package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"gophprofile/internal/config"
	"gophprofile/internal/repository/postgres"
	"gophprofile/internal/repository/s3"
	"gophprofile/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	if err := postgres.Migrate(cfg.Postgres.DSN, migrations.FS); err != nil {
		logger.Error("failed to apply migrations", "err", err)
		os.Exit(1)
	}
	logger.Info("migrations applied")

	poolCtx, poolCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := postgres.NewPool(poolCtx, cfg.Postgres.DSN)
	poolCancel()
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("postgres pool ready")

	s3Client, err := s3.NewClient(cfg.S3)
	if err != nil {
		logger.Error("failed to init s3 client", "err", err)
		os.Exit(1)
	}
	s3Ctx, s3Cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := s3Client.EnsureBucket(s3Ctx); err != nil {
		s3Cancel()
		logger.Error("failed to ensure s3 bucket", "err", err, "bucket", cfg.S3.Bucket)
		os.Exit(1)
	}
	s3Cancel()
	logger.Info("s3 bucket ready", "bucket", cfg.S3.Bucket)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", healthHandler(pool, s3Client))

	fs := http.FileServer(http.Dir("web/static"))
	r.Handle("/*", fs)

	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      r,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	go func() {
		logger.Info("http server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutdown signal received")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}

func healthHandler(pool *pgxpool.Pool, s3Client *s3.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		pgStatus := "ok"
		if err := pool.Ping(ctx); err != nil {
			pgStatus = "down"
		}

		s3Status := "ok"
		if err := s3Client.Ping(ctx); err != nil {
			s3Status = "down"
		}

		overall := "ok"
		statusCode := http.StatusOK
		if pgStatus != "ok" || s3Status != "ok" {
			overall = "degraded"
			statusCode = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": overall,
			"components": map[string]string{
				"postgres": pgStatus,
				"s3":       s3Status,
				"rabbit":   "unknown",
			},
		})
	}
}
