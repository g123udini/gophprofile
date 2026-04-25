package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
	if err := run(logger); err != nil {
		logger.Error("server exiting with error", "err", err)
		os.Exit(1)
	}
}

// run owns the full server lifecycle: every resource it acquires is released
// via defer on exit, even when startup fails. Returning an error from here
// instead of calling os.Exit ensures the tracing exporter flushes its batch.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	tracingCtx, tracingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownTracing, err := tracing.Init(tracingCtx, "gophprofile-server", logging.Version(), cfg.OTel.Endpoint, cfg.OTel.Insecure)
	tracingCancel()
	if err != nil {
		return fmt.Errorf("init tracing: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(ctx); err != nil {
			logger.Error("tracing shutdown failed", "err", err)
		}
	}()

	if err := postgres.Migrate(cfg.Postgres.DSN, migrations.FS); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	logger.Info("migrations applied")

	poolCtx, poolCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := postgres.NewPool(poolCtx, cfg.Postgres.DSN)
	poolCancel()
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer pool.Close()
	metrics.RegisterPgxPool(pool)
	logger.Info("postgres pool ready")

	s3Client, err := s3.NewClient(cfg.S3)
	if err != nil {
		return fmt.Errorf("init s3 client: %w", err)
	}
	s3Ctx, s3Cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := s3Client.EnsureBucket(s3Ctx); err != nil {
		s3Cancel()
		return fmt.Errorf("ensure s3 bucket %q: %w", cfg.S3.Bucket, err)
	}
	s3Cancel()
	logger.Info("s3 bucket ready", "bucket", cfg.S3.Bucket)

	publisher, err := rabbitmq.NewPublisher(cfg.Rabbit.URL, cfg.Rabbit.Exchange)
	if err != nil {
		return fmt.Errorf("init rabbitmq publisher: %w", err)
	}
	defer publisher.Close()
	logger.Info("rabbitmq publisher ready", "exchange", cfg.Rabbit.Exchange)

	avatarRepo := postgres.NewAvatarRepository(pool)
	avatarSvc := service.NewAvatarService(avatarRepo, s3Client, publisher)
	avatarHandler := handlers.NewAvatarHandler(avatarSvc)

	r := chi.NewRouter()
	// otelhttp must run before our own middlewares so trace_id is on ctx
	// when RequestID/Metrics/handlers log or emit child spans.
	r.Use(otelHTTPMiddleware("server"))
	// otelRouteEnricher runs AFTER chi has matched the route, so it can
	// rename the span and tag http.route with the chi pattern.
	r.Use(otelRouteEnricher)
	r.Use(apimw.RequestID)
	r.Use(otelRequestIDAttr)
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

	var listenErr error
	select {
	case err := <-srvErr:
		listenErr = err
	case sig := <-quit:
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
	logger.Info("server stopped")

	if listenErr != nil {
		return fmt.Errorf("http server: %w", listenErr)
	}
	return nil
}

// otelHTTPMiddleware wraps each request in an otelhttp span. The span gets a
// placeholder name here (chi hasn't routed yet); otelRouteEnricher renames it
// once the route pattern is known. /metrics and /health are filtered out —
// they're high-frequency scrapes that would dwarf real traffic in trace UIs.
func otelHTTPMiddleware(service string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, service,
			otelhttp.WithFilter(func(r *http.Request) bool {
				return !strings.HasPrefix(r.URL.Path, "/metrics") &&
					!strings.HasPrefix(r.URL.Path, "/health")
			}),
		)
	}
}

// otelRouteEnricher runs as a chi middleware, so by the time it executes chi
// has already matched the route and populated RoutePattern. It updates the
// already-active otelhttp span with a stable, low-cardinality name and the
// http.route attribute (semconv).
func otelRouteEnricher(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			span := trace.SpanFromContext(r.Context())
			span.SetName(r.Method + " " + rc.RoutePattern())
			span.SetAttributes(attribute.String("http.route", rc.RoutePattern()))
		}
		next.ServeHTTP(w, r)
	})
}

// otelRequestIDAttr stamps the inbound request_id (set by apimw.RequestID)
// onto the active span so traces and logs are joinable on this attribute.
func otelRequestIDAttr(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := logging.RequestIDFromContext(r.Context()); id != "" {
			trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("request_id", id))
		}
		next.ServeHTTP(w, r)
	})
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
