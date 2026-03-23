package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	// Create SSE hub and web server (always-on, independent of schedule).
	hub := web.NewSSEHub(logger)
	server := web.NewServer(store, hub, cfg.HTTPPort, sched, logger)

	// Start SSE hub.
	go hub.Run(ctx)

	// Start web server (always-on).
	go func() {
		if err := server.Run(ctx); err != nil {
			logger.Error("web server error", "error", err)
			os.Exit(1)
		}
	}()

	// On-demand pipeline: driven by SSE client presence.
	if sched == nil {
		runOnDemand(ctx, nil, cfg, store, hub, server, logger)
	} else {
		for {
			if err := sched.WaitForWindow(ctx, logger); err != nil {
				logger.Info("shutdown complete")
				return
			}
			schedCtx, schedCancel := sched.ContextUntil(ctx, logger)
			runOnDemand(schedCtx, sched, cfg, store, hub, server, logger)
			schedCancel()
			logger.Info("schedule window ended, waiting for next window")
		}
	}
}

const (
	gracePeriod      = 3 * time.Minute
	minPipelineRun   = 20 * time.Minute
)

// runOnDemand starts/stops the pipeline based on SSE client presence.
// It blocks until ctx is cancelled (schedule end or shutdown).
func runOnDemand(ctx context.Context, sched *schedule.Schedule, cfg config.Config, store *storage.Client, hub *web.SSEHub, server *web.Server, logger *slog.Logger) {
	var (
		pipeCancel     context.CancelFunc
		pipelineDone   chan error
		running        bool
		graceTimer     *time.Timer
		pendingRestart bool
		pipeStartedAt  time.Time
	)

	startPipeline := func() {
		pipeCtx, cancel := context.WithCancel(ctx)
		pipeCancel = cancel
		pipelineDone = make(chan error, 1)
		running = true
		pendingRestart = false
		pipeStartedAt = time.Now()
		server.SetPipelineRunning(true)
		go func() {
			pipelineDone <- pipeline.Run(pipeCtx, cfg, store, hub, server, logger, hub.NotifyStatus)
		}()
		logger.Info("pipeline started (visitor connected)")
	}

	schedActive := func() bool {
		return sched == nil || sched.IsActive(time.Now())
	}

	notifyOffSchedule := func() {
		hub.NotifyStatus("Hors plage horaire. Reprise à " + sched.NextStart(time.Now()).Format("15:04") + " (heure de La Réunion)")
	}

	// Check if clients are already connected at the start of this window.
	if hub.ClientCount() > 0 {
		if schedActive() {
			startPipeline()
		} else {
			notifyOffSchedule()
		}
	}

	// Drain stale presence signals queued during the ClientCount() check.
	for {
		select {
		case <-hub.Presence():
		default:
			goto eventLoop
		}
	}

eventLoop:
	for {
		select {
		case wake := <-hub.Presence():
			if wake {
				// Client connected (0→1 transition).
				if running && graceTimer != nil {
					if graceTimer.Stop() {
						// Timer stopped before firing — pipeline still healthy.
						graceTimer = nil
						logger.Info("grace period cancelled, visitor reconnected")
					} else {
						// Timer already fired — pipeCancel() was called, pipeline is shutting down.
						// Flag restart for when pipelineDone arrives.
						graceTimer = nil
						pendingRestart = true
						logger.Info("grace period already expired, will restart pipeline after shutdown")
					}
				} else if !running {
					if !schedActive() {
						notifyOffSchedule()
					} else {
						startPipeline()
					}
				}
			} else {
				// Last client disconnected (N→0 transition).
				if running {
					// Ensure the pipeline runs for at least minPipelineRun so
					// it has time to produce articles, even if the visitor leaves quickly.
					delay := max(time.Until(pipeStartedAt.Add(minPipelineRun)), gracePeriod)
					cancel := pipeCancel
					graceTimer = time.AfterFunc(delay, func() {
						logger.Info("shutdown timer expired, stopping pipeline")
						cancel()
					})
					logger.Info("shutdown timer started", "delay", delay.Round(time.Second))
				}
			}

		case err := <-pipelineDone:
			running = false
			server.SetPipelineRunning(false)
			if pipeCancel != nil {
				pipeCancel()
				pipeCancel = nil
			}
			if graceTimer != nil {
				graceTimer.Stop()
				graceTimer = nil
			}
			pipelineDone = nil
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				logger.Warn("pipeline stopped", "error", err)
			} else {
				logger.Info("pipeline stopped")
			}
			// Restart if a visitor reconnected while the pipeline was shutting down.
			if pendingRestart {
				pendingRestart = false
				if schedActive() {
					startPipeline()
				}
			}

		case <-ctx.Done():
			if graceTimer != nil {
				graceTimer.Stop()
			}
			if pipeCancel != nil {
				pipeCancel()
			}
			if running && pipelineDone != nil {
				<-pipelineDone
			}
			server.SetPipelineRunning(false)
			return
		}
	}
}
