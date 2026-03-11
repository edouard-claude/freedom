package transcribe

import (
	"context"
	"log/slog"
	"time"

	"freedom/internal/chunk"
)

// WorkerPool runs N concurrent transcription workers.
type WorkerPool struct {
	client  *Client
	workers int
	logger  *slog.Logger
}

// NewWorkerPool creates a worker pool.
func NewWorkerPool(client *Client, workers int, logger *slog.Logger) *WorkerPool {
	return &WorkerPool{
		client:  client,
		workers: workers,
		logger:  logger,
	}
}

// Run starts workers that read chunks and produce transcription results.
// The caller is responsible for closing chunkCh. resultCh is closed when
// all workers finish.
func (wp *WorkerPool) Run(ctx context.Context, chunkCh <-chan chunk.Chunk, resultCh chan<- TranscriptionResult) error {
	done := make(chan struct{}, wp.workers)

	for i := range wp.workers {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			wp.worker(ctx, id, chunkCh, resultCh)
		}(i)
	}

	// Wait for all workers.
	for range wp.workers {
		<-done
	}
	close(resultCh)
	return nil
}

func (wp *WorkerPool) worker(ctx context.Context, id int, chunkCh <-chan chunk.Chunk, resultCh chan<- TranscriptionResult) {
	for {
		select {
		case <-ctx.Done():
			return
		case ch, ok := <-chunkCh:
			if !ok {
				return
			}

			wp.logger.Debug("transcribing chunk", "worker", id, "seq", ch.SeqNum, "duration", ch.Duration)
			start := time.Now()

			text, err := wp.client.Transcribe(ctx, ch)
			latency := time.Since(start)

			result := TranscriptionResult{
				SeqNum:   ch.SeqNum,
				Text:     text,
				RawMP3:   ch.RawMP3,
				Duration: ch.Duration,
				Latency:  latency,
				Err:      err,
			}

			if err != nil {
				wp.logger.Error("transcription failed", "worker", id, "seq", ch.SeqNum, "error", err)
			} else {
				wp.logger.Info("transcription complete", "worker", id, "seq", ch.SeqNum, "latency", latency)
			}

			select {
			case resultCh <- result:
			case <-ctx.Done():
				return
			}
		}
	}
}
