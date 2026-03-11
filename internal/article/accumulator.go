package article

import (
	"context"
	"strings"
)

// Accumulator collects transcription segments into sliding windows.
type Accumulator struct {
	windowSize int
}

// NewAccumulator creates an accumulator with the given window size.
func NewAccumulator(windowSize int) *Accumulator {
	return &Accumulator{windowSize: windowSize}
}

// Run reads segments from segCh, accumulates them into windows of windowSize,
// and sends combined text+audio on windowCh. Uses 50% overlap between windows.
func (a *Accumulator) Run(ctx context.Context, segCh <-chan Segment, windowCh chan<- Window) error {
	defer close(windowCh)

	var buf []Segment

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case seg, ok := <-segCh:
			if !ok {
				return nil
			}
			buf = append(buf, seg)

			if len(buf) >= a.windowSize {
				// Combine texts.
				texts := make([]string, len(buf))
				for i, s := range buf {
					texts[i] = s.Text
				}

				// Concatenate audio.
				totalSize := 0
				for _, s := range buf {
					totalSize += len(s.RawMP3)
				}
				raw := make([]byte, 0, totalSize)
				for _, s := range buf {
					raw = append(raw, s.RawMP3...)
				}

				w := Window{
					Text:     strings.Join(texts, " "),
					RawMP3:   raw,
					Segments: append([]Segment(nil), buf...),
				}
				select {
				case windowCh <- w:
				case <-ctx.Done():
					return ctx.Err()
				}
				// Slide: keep second half for overlap.
				half := a.windowSize / 2
				buf = append([]Segment(nil), buf[half:]...)
			}
		}
	}
}
