package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gophprofile/internal/broker/rabbitmq"
	"gophprofile/internal/config"
	"gophprofile/internal/events"
	"gophprofile/internal/logging"
	"gophprofile/internal/metrics"
	"gophprofile/internal/observability/tracing"
	"gophprofile/internal/repository/postgres"
	"gophprofile/internal/repository/s3"
	"gophprofile/internal/worker"
)

const (
	queueName        = "avatars.processing"
	routingKey       = "avatar.uploaded"
	metricsAddr      = ":8081"
	consumerPrefetch = 1
)

func main() {
	logger := logging.New("worker", logging.Version())
	if err := run(logger); err != nil {
		logger.Error("worker exiting with error", "err", err)
		os.Exit(1)
	}
}

// run owns the full worker lifecycle: every resource it acquires is released
// via defer on exit, even when startup fails. Returning an error from here
// instead of calling os.Exit ensures the tracing exporter flushes its batch.
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	tracingCtx, tracingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownTracing, err := tracing.Init(tracingCtx, "gophprofile-worker", logging.Version(), cfg.OTel.Endpoint, cfg.OTel.Insecure)
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
	logger.Info("s3 client ready", "bucket", cfg.S3.Bucket)

	consumer, err := rabbitmq.NewConsumer(cfg.Rabbit.URL, cfg.Rabbit.Exchange, queueName, routingKey)
	if err != nil {
		return fmt.Errorf("init rabbitmq consumer: %w", err)
	}
	defer consumer.Close()
	metrics.RabbitConsumerPrefetch.Set(consumerPrefetch)
	logger.Info("rabbitmq consumer ready", "queue", queueName, "routing_key", routingKey)

	repo := postgres.NewAvatarRepository(pool)
	processor := worker.NewAvatarProcessor(s3Client, repo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metricsSrv := &http.Server{
		Addr:              metricsAddr,
		Handler:           metricsMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	metricsErr := make(chan error, 1)
	go func() {
		logger.Info("metrics server starting", "addr", metricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			metricsErr <- err
			cancel()
		}
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("metrics server shutdown failed", "err", err)
		}
	}()

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		select {
		case sig := <-quit:
			logger.Info("shutdown signal received", "signal", sig.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	logger.Info("worker starting")
	runErr := consumer.Run(ctx, func(ctx context.Context, evt events.AvatarUploadedEvent) error {
		logger.InfoContext(ctx, "processing avatar", "avatar_id", evt.AvatarID, "user_id", evt.UserID)
		return processor.HandleUploaded(ctx, evt)
	})
	logger.Info("worker stopped")

	// Surface a metrics-server failure (which would have triggered cancel())
	// over a clean ctx-canceled return from the consumer.
	select {
	case err := <-metricsErr:
		return fmt.Errorf("metrics server: %w", err)
	default:
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf("consumer: %w", runErr)
	}
	return nil
}

func metricsMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	return mux
}
