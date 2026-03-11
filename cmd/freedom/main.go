package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"freedom/internal/config"
	"freedom/internal/pipeline"
	"freedom/internal/storage"
	"freedom/internal/web"
)

func main() {
	cfg, err := config.Parse()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	// Setup structured logger.
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Signal-aware context.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Create S3/MinIO client.
	store, err := storage.NewClient(storage.S3Config{
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    cfg.S3Bucket,
		UseSSL:    cfg.S3UseSSL,
	}, logger)
	if err != nil {
		logger.Error("storage initialization failed", "error", err)
		os.Exit(1)
	}

	// Create SSE hub and web server.
	hub := web.NewSSEHub(logger)
	server := web.NewServer(store, hub, cfg.HTTPPort, logger)

	logger.Info("starting freedom",
		"stream", cfg.StreamURL,
		"chunk_duration", cfg.ChunkDuration,
		"overlap", cfg.Overlap,
		"workers", cfg.Workers,
		"language", cfg.Language,
		"transcribe_model", cfg.TranscribeModel,
		"classify_model", cfg.ClassifyModel,
		"article_model", cfg.ArticleModel,
		"http_port", cfg.HTTPPort,
	)

	if err := pipeline.Run(ctx, cfg, store, hub, server, logger); err != nil {
		if ctx.Err() != nil {
			logger.Info("shutdown complete")
			return
		}
		logger.Error("pipeline error", "error", err)
		os.Exit(1)
	}
}
