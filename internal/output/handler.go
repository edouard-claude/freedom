package output

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"freedom/internal/article"
	"freedom/internal/transcribe"
)

const sequenceTimeout = 30 * time.Second

// Handler reorders transcription results by sequence number and forwards
// segments to the article accumulator.
type Handler struct {
	logger       *slog.Logger
	segAccumCh   chan<- article.Segment
	transcriptCh chan<- string
}

// NewHandler creates an output handler. segAccumCh receives transcribed segments
// for article generation. transcriptCh receives raw transcript text for SSE streaming.
func NewHandler(logger *slog.Logger, segAccumCh chan<- article.Segment, transcriptCh chan<- string) *Handler {
	return &Handler{logger: logger, segAccumCh: segAccumCh, transcriptCh: transcriptCh}
}

// Run reads results, reorders by SeqNum, and prints to stdout.
func (h *Handler) Run(ctx context.Context, resultCh <-chan transcribe.TranscriptionResult) error {
	pending := make(map[uint64]transcribe.TranscriptionResult)
	var nextSeq uint64
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case result, ok := <-resultCh:
			if !ok {
				// Flush any remaining pending results in order.
				for {
					r, exists := pending[nextSeq]
					if !exists {
						break
					}
					h.printResult(r, startTime)
					delete(pending, nextSeq)
					nextSeq++
				}
				return nil
			}

			pending[result.SeqNum] = result

			// Emit all consecutive results.
			for {
				r, exists := pending[nextSeq]
				if !exists {
					break
				}
				h.printResult(r, startTime)
				delete(pending, nextSeq)
				nextSeq++
			}

		case <-time.After(sequenceTimeout):
			// If we've been waiting too long for the next sequence, skip it.
			if _, exists := pending[nextSeq]; !exists && len(pending) > 0 {
				h.logger.Warn("sequence timeout, skipping", "seq", nextSeq)
				nextSeq++
				// Try to emit again.
				for {
					r, exists := pending[nextSeq]
					if !exists {
						break
					}
					h.printResult(r, startTime)
					delete(pending, nextSeq)
					nextSeq++
				}
			}
		}
	}
}

func (h *Handler) printResult(r transcribe.TranscriptionResult, startTime time.Time) {
	elapsed := time.Since(startTime).Truncate(time.Second)
	if r.Err != nil {
		h.logger.Error("chunk failed", "seq", r.SeqNum, "error", r.Err)
		return
	}
	if r.Text == "" {
		return
	}
	fmt.Printf("[%s] %s\n", formatDuration(elapsed), r.Text)

	// Forward transcript for live SSE streaming (non-blocking).
	if h.transcriptCh != nil {
		select {
		case h.transcriptCh <- r.Text:
		default:
		}
	}

	// Forward segment for article generation (non-blocking).
	if h.segAccumCh != nil {
		select {
		case h.segAccumCh <- article.Segment{Text: r.Text, RawMP3: r.RawMP3}:
		default:
			h.logger.Warn("segment dropped, article pipeline behind")
		}
	}
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
