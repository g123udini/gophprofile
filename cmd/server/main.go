package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	apimw "gophprofile/internal/api/middleware"
	"gophprofile/internal/broker/rabbitmq"
	"gophprofile/internal/config"
	"gophprofile/internal/handlers"
	"gophprofile/internal/logging"
	"gophprofile/internal/metrics"
	"gophprofile/internal/observability/tracing"
	"gophprofile/internal/repository/postgres"
	"gophprofile/internal/repository/s3"
	"gophprofile/internal/service"
	"gophprofile/migrations"
)

func main() {
	logger := logging.New("server", logging.Version())

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	tracingCtx, tracingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownTracing, err := tracing.Init(tracingCtx, "gophprofile-server", logging.Version(), cfg.OTel.Endpoint, cfg.OTel.Insecure)
	tracingCancel()
	if err != nil {
		logger.Error("failed to init tracing", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.Error("tracing shutdown failed", "err", err)
		}
	}()

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
	metrics.RegisterPgxPool(pool)
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

	publisher, err := rabbitmq.NewPublisher(cfg.Rabbit.URL, cfg.Rabbit.Exchange)
	if err != nil {
		logger.Error("failed to init rabbitmq publisher", "err", err)
		os.Exit(1)
	}
	defer publisher.Close()
	logger.Info("rabbitmq publisher ready", "exchange", cfg.Rabbit.Exchange)

	avatarRepo := postgres.NewAvatarRepository(pool)
	avatarSvc := service.NewAvatarService(avatarRepo, s3Client, publisher)
	avatarHandler := handlers.NewAvatarHandler(avatarSvc)

	r := chi.NewRouter()
	// otelhttp must run before our own middlewares so trace_id is on ctx
	// when RequestID/Metrics/handlers log or emit child spans.
	r.Use(otelMiddleware("server"))
	r.Use(apimw.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(apimw.Metrics)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Handle("/metrics", metrics.Handler())
	r.Get("/health", healthHandler(pool, s3Client, publisher))
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/avatars", avatarHandler.Upload)
		r.Get("/avatars/{id}", avatarHandler.GetByID)
		r.Get("/avatars/{id}/metadata", avatarHandler.GetMetadata)
		r.Delete("/avatars/{id}", avatarHandler.Delete)
		r.Get("/users/{user_id}/avatar", avatarHandler.GetUserLatest)
		r.Get("/users/{user_id}/avatars", avatarHandler.ListUserAvatars)
	})

	fs := http.FileServer(http.Dir("web/static"))
	r.Handle("/*", fs)

	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      r,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	srvErr := make(chan error, 1)
	go func() {
		logger.Info("http server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srvErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-srvErr:
		logger.Error("http server failed", "err", err)
	case sig := <-quit:
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	logger.Info("server stopped")
}

// otelMiddleware wraps each handler in an otelhttp span. Span name is the chi
// route pattern when available, falling back to the request method so we don't
// leak high-cardinality URLs (e.g. avatar UUIDs) into the trace UI.
func otelMiddleware(service string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, service,
			otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
					return r.Method + " " + rc.RoutePattern()
				}
				return r.Method
			}),
		)
	}
}

func healthHandler(pool *pgxpool.Pool, s3Client *s3.Client, publisher *rabbitmq.Publisher) http.HandlerFunc {
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

		rabbitStatus := "ok"
		if !publisher.Healthy() {
			rabbitStatus = "down"
		}

		overall := "ok"
		statusCode := http.StatusOK
		if pgStatus != "ok" || s3Status != "ok" || rabbitStatus != "ok" {
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
				"rabbit":   rabbitStatus,
			},
		})
	}
}
