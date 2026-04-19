package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gophprofile/internal/broker/rabbitmq"
	"gophprofile/internal/config"
	"gophprofile/internal/events"
	"gophprofile/internal/repository/postgres"
	"gophprofile/internal/repository/s3"
	"gophprofile/internal/worker"
)

const (
	queueName  = "avatars.processing"
	routingKey = "avatar.uploaded"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

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
	logger.Info("s3 client ready", "bucket", cfg.S3.Bucket)

	consumer, err := rabbitmq.NewConsumer(cfg.Rabbit.URL, cfg.Rabbit.Exchange, queueName, routingKey)
	if err != nil {
		logger.Error("failed to init rabbitmq consumer", "err", err)
		os.Exit(1)
	}
	defer consumer.Close()
	logger.Info("rabbitmq consumer ready", "queue", queueName, "routing_key", routingKey)

	repo := postgres.NewAvatarRepository(pool)
	processor := worker.NewAvatarProcessor(s3Client, repo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		logger.Info("shutdown signal received")
		cancel()
	}()

	logger.Info("worker starting")
	err = consumer.Run(ctx, func(ctx context.Context, evt events.AvatarUploadedEvent) error {
		logger.Info("processing avatar", "avatar_id", evt.AvatarID, "user_id", evt.UserID)
		return processor.HandleUploaded(ctx, evt)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("consumer stopped with error", "err", err)
		os.Exit(1)
	}
	logger.Info("worker stopped")
}
