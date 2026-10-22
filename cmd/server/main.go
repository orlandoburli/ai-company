// Command server runs the AI Company control plane.
//
// It is the single process for the MVP: REST API, orchestrator and agent runtime
// share one binary and one database. Splitting them is a deployment choice, not
// an architectural one, and nothing here prevents it later.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/orlandoburli/ai-company/internal/api"
	"github.com/orlandoburli/ai-company/internal/store/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", envOr("AIC_ADDR", ":8080"), "listen address")
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres connection URL (defaults to $DATABASE_URL)")
	applyMigrations := flag.Bool("migrate", true, "apply pending migrations before serving")
	secureCookies := flag.Bool("secure-cookies", envBool("AIC_SECURE_COOKIES", true),
		"mark the session cookie Secure (disable only for plain-http development)")
	flag.Parse()

	if *dsn == "" {
		return errors.New("set DATABASE_URL or pass -dsn")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(log)

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// A small pool: this process is I/O bound on agent subprocesses, and Postgres
	// connections are a shared, finite resource.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	if *applyMigrations {
		ran, err := migrations.Up(ctx, db)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if len(ran) == 0 {
			log.Info("schema up to date")
		}
		for _, m := range ran {
			log.Info("migration applied", "migration", m)
		}
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(db, log, api.WithSecureCookies(*secureCookies)).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Give in-flight requests a bounded window; the orchestrator's own drain
	// (stop claiming work, finish or park running items) comes in M1.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func logLevel() slog.Level {
	switch os.Getenv("AIC_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
