// Package retention prunes old monthly partitions from append-only job logs.
package retention

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// Config controls the retention window. KeepDays defaults to 90 when zero.
type Config struct {
	KeepDays int
	Now      func() time.Time
	Logger   *slog.Logger
}

// Result reports partitions dropped and the number of range partitions kept.
type Result struct {
	Dropped []string
	Kept    int
}

type partition struct {
	name  string
	upper time.Time
}

var upperBoundPattern = regexp.MustCompile(`(?i)\bTO\s*\(\s*'((?:[^']|'')*)'`)

// Prune drops only range partitions whose exclusive upper bound is at or before
// the retention cutoff. The bound guarantees no retained-window rows can be in
// such a partition. It is a no-op if public.job_logs is not partitioned.
func Prune(ctx context.Context, db *sql.DB, cfg Config) (Result, error) {
	var result Result
	if db == nil {
		return result, fmt.Errorf("retention: nil database")
	}
	if cfg.KeepDays == 0 {
		cfg.KeepDays = 90
	}
	if cfg.KeepDays < 0 {
		return result, fmt.Errorf("retention: KeepDays must not be negative")
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	cutoff := now().AddDate(0, 0, -cfg.KeepDays)

	var partitioned bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM pg_partitioned_table p
		    JOIN pg_class c ON c.oid = p.partrelid
	    	JOIN pg_namespace n ON n.oid = c.relnamespace
		    WHERE n.nspname = 'public' AND c.relname = 'job_logs'
		)`).Scan(&partitioned)
	if err != nil {
		return result, fmt.Errorf("retention: inspect job_logs partitioning: %w", err)
	}
	if !partitioned {
		return result, nil
	}

	rows, err := db.QueryContext(ctx, `
		SELECT child.relname, pg_get_expr(child.relpartbound, child.oid)
		  FROM pg_inherits i
		  JOIN pg_class parent ON parent.oid = i.inhparent
		  JOIN pg_namespace pn ON pn.oid = parent.relnamespace
		  JOIN pg_class child ON child.oid = i.inhrelid
		 WHERE pn.nspname = 'public' AND parent.relname = 'job_logs'
		 ORDER BY child.relname`)
	if err != nil {
		return result, fmt.Errorf("retention: list job_logs partitions: %w", err)
	}
	var partitions []partition
	for rows.Next() {
		var p partition
		var bound string
		if err := rows.Scan(&p.name, &bound); err != nil {
			rows.Close()
			return result, fmt.Errorf("retention: scan partition: %w", err)
		}
		// DEFAULT has no upper range boundary and must never be pruned here.
		match := upperBoundPattern.FindStringSubmatch(bound)
		if match == nil {
			continue
		}
		upperText := strings.ReplaceAll(match[1], "''", "'")
		p.upper, err = parsePartitionTimestamp(upperText)
		if err != nil {
			rows.Close()
			return result, fmt.Errorf("retention: parse upper bound for %s (%q): %w", p.name, bound, err)
		}
		partitions = append(partitions, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("retention: read partitions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("retention: close partition rows: %w", err)
	}

	for _, p := range partitions {
		if !p.upper.Before(cutoff) && !p.upper.Equal(cutoff) {
			result.Kept++
			continue
		}
		if err := dropPartition(ctx, db, p.name); err != nil {
			return result, fmt.Errorf("retention: drop partition %s: %w", p.name, err)
		}
		result.Dropped = append(result.Dropped, p.name)
		if cfg.Logger != nil {
			cfg.Logger.InfoContext(ctx, "dropped old job log partition", "partition", p.name)
		}
	}
	return result, nil
}

func dropPartition(ctx context.Context, db *sql.DB, name string) error {
	// Names originate from pg_class and are still quoted as identifiers. No
	// CASCADE is used: references from other tables make the drop fail safely.
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.ExecContext(ctx, `ALTER TABLE public.job_logs DETACH PARTITION public.`+quoted); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE public.`+quoted); err != nil {
		return err
	}
	return tx.Commit()
}

func parsePartitionTimestamp(value string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported PostgreSQL timestamp %q", value)
}
