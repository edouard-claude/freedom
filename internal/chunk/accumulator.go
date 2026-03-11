package chunk

import (
	"context"
	"time"

	"freedom/internal/mp3"
)

// Accumulator collects MP3 frames until targetDuration is reached, then emits a Chunk.
type Accumulator struct {
	targetDuration time.Duration
	overlapDur     time.Duration
}

// NewAccumulator creates an accumulator.
func NewAccumulator(targetDuration, overlap time.Duration) *Accumulator {
	return &Accumulator{
		targetDuration: targetDuration,
		overlapDur:     overlap,
	}
}

// Run reads frames from frameCh and emits chunks on chunkCh.
func (a *Accumulator) Run(ctx context.Context, frameCh <-chan mp3.Frame, chunkCh chan<- Chunk) error {
	defer close(chunkCh)

	var (
		seqNum   uint64
		frames   []mp3.Frame
		totalDur time.Duration
	)

	emit := func() {
		if len(frames) == 0 {
			return
		}

		// Calculate total size.
		size := 0
		for _, f := range frames {
			size += len(f.RawBytes)
		}

		raw := make([]byte, 0, size)
		for _, f := range frames {
			raw = append(raw, f.RawBytes...)
		}

		c := Chunk{
			SeqNum:   seqNum,
			RawMP3:   raw,
			Duration: totalDur,
		}
		seqNum++

		select {
		case chunkCh <- c:
		case <-ctx.Done():
			return
		}

		// Keep overlap frames for continuity.
		if a.overlapDur > 0 {
			var overlapFrames []mp3.Frame
			var overlapDur time.Duration
			for i := len(frames) - 1; i >= 0; i-- {
				overlapDur += frames[i].Duration
				if overlapDur >= a.overlapDur {
					overlapFrames = make([]mp3.Frame, len(frames)-i)
					copy(overlapFrames, frames[i:])
					break
				}
			}
			frames = overlapFrames
			totalDur = overlapDur
		} else {
			frames = frames[:0]
			totalDur = 0
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-frameCh:
			if !ok {
				emit()
				return nil
			}
			frames = append(frames, frame)
			totalDur += frame.Duration
			if totalDur >= a.targetDuration {
				emit()
			}
		}
	}
}
