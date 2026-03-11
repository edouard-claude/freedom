package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"freedom/internal/config"
	"freedom/internal/pipeline"
	"freedom/internal/schedule"
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
		"schedule", cfg.Schedule,
	)

	// Parse schedule if configured.
	var sched *schedule.Schedule
	if cfg.Schedule != "" {
		s, err := schedule.Parse(cfg.Schedule, cfg.ScheduleTimezone)
		if err != nil {
			logger.Error("invalid schedule", "error", err)
			os.Exit(1)
		}
		sched = &s
		logger.Info("schedule configured",
			"window", cfg.Schedule,
			"timezone", cfg.ScheduleTimezone,
		)
	}

	// Run loop: wait for schedule window, run pipeline, repeat.
	for {
		if err := runOnce(ctx, sched, cfg, store, hub, server, logger); err != nil {
			if ctx.Err() != nil {
				logger.Info("shutdown complete")
				return
			}
			logger.Error("pipeline error", "error", err)
			os.Exit(1)
		}
		// No schedule → single run, exit cleanly.
		if sched == nil {
			return
		}
	}
}

func runOnce(ctx context.Context, sched *schedule.Schedule, cfg config.Config, store *storage.Client, hub *web.SSEHub, server *web.Server, logger *slog.Logger) error {
	runCtx := ctx

	if sched != nil {
		if err := sched.WaitForWindow(ctx, logger); err != nil {
			return err
		}
		var cancel context.CancelFunc
		runCtx, cancel = sched.ContextUntil(ctx, logger)
		defer cancel()
	}

	err := pipeline.Run(runCtx, cfg, store, hub, server, logger)
	if err != nil && sched != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		// Schedule window ended — not a real error.
		logger.Info("schedule window ended, waiting for next window")
		return nil
	}
	return err
}
