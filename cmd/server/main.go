package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/migrate"
	"github.com/nesono/evidence-store/internal/retention"
	"github.com/nesono/evidence-store/internal/server"
	"github.com/nesono/evidence-store/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Run database migrations.
	migrationsPath := os.Getenv("EVIDENCE_MIGRATIONS_PATH")
	if migrationsPath == "" {
		migrationsPath = "migrations"
	}
	if err := migrate.Run(cfg.DatabaseURL, migrationsPath); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	slog.Info("database connected")

	// Seed the first administrator before the server starts taking requests:
	// with EVIDENCE_AUTH_DB on, every endpoint is closed, and the API for
	// issuing keys is itself an administrator's.
	if cfg.Auth.DB && cfg.Auth.BootstrapAdmin != "" {
		key, err := auth.BootstrapAdmin(ctx, store.NewPrincipalStore(pool), cfg.Auth.BootstrapAdmin, slog.Default())
		if err != nil {
			slog.Error("failed to bootstrap admin principal", "error", err)
			os.Exit(1)
		}
		if key != "" {
			// The one moment this value is readable. It is deliberately in the
			// log and nowhere else: there is no second copy to leak, and an
			// operator who misses it disables the principal and seeds another.
			slog.Warn("bootstrap admin API key issued - copy it now, it is not stored and will not be shown again",
				"subject", cfg.Auth.BootstrapAdmin, "api_key", key)
		}
	}

	blobs, err := blob.Open(ctx, cfg.Blob.Options)
	if err != nil {
		slog.Error("failed to open blob store", "error", err)
		os.Exit(1)
	}
	slog.Info("blob store ready", "backend", cfg.Blob.Options.Backend)

	srv := server.New(cfg, pool, blobs)

	// Start retention worker if configured.
	if retentionPath := os.Getenv("EVIDENCE_RETENTION_CONFIG"); retentionPath != "" {
		retCfg, err := retention.LoadConfig(retentionPath)
		if err != nil {
			slog.Error("failed to load retention config", "error", err, "path", retentionPath)
			os.Exit(1)
		}
		evidenceStore := store.NewEvidenceStore(pool)
		inheritanceStore := store.NewInheritanceStore(pool)
		worker, err := retention.NewWorker(retCfg, evidenceStore, inheritanceStore, slog.Default())
		if err != nil {
			slog.Error("failed to create retention worker", "error", err)
			os.Exit(1)
		}
		// Blobs only become unreachable when the records naming them are
		// deleted, so the sweep rides along with retention rather than running
		// on a schedule of its own.
		worker = worker.WithBlobs(blobs, store.NewBlobRefStore(pool), cfg.Blob.OrphanGrace)
		go worker.Start(ctx)
		slog.Info("retention worker configured", "config", retentionPath, "interval", retCfg.Interval)
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)
}
