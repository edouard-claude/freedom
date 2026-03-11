package mp3

import (
	"context"
	"log/slog"
	"time"
)

const (
	ringSize     = 128 * 1024 // 128KB ring buffer
	compactThres = ringSize / 2
)

// Parser reads raw audio bytes and emits parsed MP3 frames.
type Parser struct {
	buf    []byte
	rpos   int // read position
	wpos   int // write position
	logger *slog.Logger
}

// NewParser creates a new MP3 frame parser.
func NewParser(logger *slog.Logger) *Parser {
	return &Parser{
		buf:    make([]byte, ringSize),
		logger: logger,
	}
}

// Run reads raw byte slices from rawCh, parses MP3 frames, and sends them to frameCh.
// It returns the buffer slices to the pool via returnFn.
func (p *Parser) Run(ctx context.Context, rawCh <-chan []byte, frameCh chan<- Frame, returnFn func([]byte)) error {
	defer close(frameCh)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case data, ok := <-rawCh:
			if !ok {
				// Drain remaining frames.
				p.scanFrames(ctx, frameCh)
				return nil
			}
			p.ingest(data)
			returnFn(data)
			p.scanFrames(ctx, frameCh)
		}
	}
}

func (p *Parser) ingest(data []byte) {
	avail := len(p.buf) - p.wpos
	if avail < len(data) {
		p.compact()
		avail = len(p.buf) - p.wpos
	}
	if len(data) > avail {
		// Extremely large chunk -- should not happen with 32KB reads into 128KB buffer.
		data = data[:avail]
	}
	copy(p.buf[p.wpos:], data)
	p.wpos += len(data)
}

func (p *Parser) compact() {
	n := copy(p.buf, p.buf[p.rpos:p.wpos])
	p.wpos = n
	p.rpos = 0
}

func (p *Parser) readable() int {
	return p.wpos - p.rpos
}

func (p *Parser) scanFrames(ctx context.Context, frameCh chan<- Frame) {
	for {
		if ctx.Err() != nil {
			return
		}
		if p.readable() < 4 {
			return
		}

		// Find sync word.
		synced := false
		for p.rpos+3 < p.wpos {
			if p.buf[p.rpos] == 0xFF && (p.buf[p.rpos+1]&0xE0) == 0xE0 {
				synced = true
				break
			}
			p.rpos++
		}
		if !synced {
			return
		}

		var hdr4 [4]byte
		copy(hdr4[:], p.buf[p.rpos:p.rpos+4])

		h, err := ParseHeader(hdr4)
		if err != nil {
			// Bad header at this sync -- skip byte and retry.
			p.rpos++
			continue
		}

		frameSize := h.FrameSize()
		if frameSize < 4 || frameSize > 4608 {
			// Unreasonable frame size -- skip.
			p.rpos++
			continue
		}

		// Do we have the full frame?
		if p.readable() < frameSize {
			return
		}

		// Two-frame validation: check for valid sync at next frame boundary.
		nextOffset := p.rpos + frameSize
		if p.wpos > nextOffset+3 {
			// We have enough data to check the next header.
			if p.buf[nextOffset] != 0xFF || (p.buf[nextOffset+1]&0xE0) != 0xE0 {
				// No valid sync at expected next frame -- false positive.
				p.rpos++
				continue
			}
			// Parse next header to further validate.
			var next4 [4]byte
			copy(next4[:], p.buf[nextOffset:nextOffset+4])
			if _, err := ParseHeader(next4); err != nil {
				p.rpos++
				continue
			}
		}
		// If we don't have enough data for two-frame validation, accept the frame
		// (we're at the tail of available data).

		// Copy frame bytes.
		raw := make([]byte, frameSize)
		copy(raw, p.buf[p.rpos:p.rpos+frameSize])
		p.rpos += frameSize

		dur := time.Duration(float64(time.Second) * h.Duration())

		frame := Frame{
			RawBytes:   raw,
			Bitrate:    h.Bitrate,
			SampleRate: h.SampleRate,
			Duration:   dur,
			Samples:    h.Samples,
		}

		select {
		case frameCh <- frame:
		case <-ctx.Done():
			return
		}

		// Compact if read position passed threshold.
		if p.rpos > compactThres {
			p.compact()
		}
	}
}
