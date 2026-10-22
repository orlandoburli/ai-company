// Command migrate applies the embedded SQL migrations to a Postgres database.
//
// Usage:
//
//	DATABASE_URL=postgres://... go run ./cmd/migrate
//	go run ./cmd/migrate -dsn "postgres://..."
//
// Migrations are plain SQL files in internal/store/migrations, applied once each
// in version order and recorded in schema_migrations. A failed migration rolls
// back and is not recorded, so re-running is safe.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/orlandoburli/ai-company/internal/store/migrations"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
	timeout := flag.Duration("timeout", 60*time.Second, "overall timeout")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "migrate: set DATABASE_URL or pass -dsn")
		os.Exit(2)
	}

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	ran, err := migrations.Up(ctx, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}

	if len(ran) == 0 {
		fmt.Println("migrate: schema up to date")
		return
	}
	for _, m := range ran {
		fmt.Println("migrate: applied", m)
	}
}
