package icecast

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const (
	readBufSize    = 32 * 1024 // 32KB reads
	backoffMin     = 1 * time.Second
	backoffMax     = 30 * time.Second
	backoffFactor  = 2.0
	jitterFraction = 0.25
)

// Reader connects to an Icecast stream and sends raw audio bytes on a channel.
type Reader struct {
	url    string
	logger *slog.Logger
	getBuf func() []byte
	putBuf func([]byte)
}

// NewReader creates an Icecast stream reader.
func NewReader(url string, logger *slog.Logger, getBuf func() []byte, putBuf func([]byte)) *Reader {
	return &Reader{
		url:    url,
		logger: logger,
		getBuf: getBuf,
		putBuf: putBuf,
	}
}

// Run connects to the stream and sends audio-only bytes on rawCh.
// It reconnects with exponential backoff on errors.
func (r *Reader) Run(ctx context.Context, rawCh chan<- []byte) error {
	defer close(rawCh)

	backoff := backoffMin
	for {
		err := r.stream(ctx, rawCh)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.logger.Warn("stream disconnected, reconnecting", "error", err, "backoff", backoff)

		jitter := time.Duration(float64(backoff) * jitterFraction * (2*rand.Float64() - 1))
		wait := backoff + jitter

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		backoff = time.Duration(float64(backoff) * backoffFactor)
		if backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

func (r *Reader) stream(ctx context.Context, rawCh chan<- []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Icy-MetaData", "1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Parse ICY metadata interval.
	metaInt := 0
	if v := resp.Header.Get("Icy-Metaint"); v != "" {
		metaInt, _ = strconv.Atoi(v)
	}

	stripper := NewMetadataStripper(metaInt)
	r.logger.Info("connected to stream", "url", r.url, "metaint", metaInt,
		"content-type", resp.Header.Get("Content-Type"))

	readBuf := make([]byte, readBufSize)
	for {
		n, err := resp.Body.Read(readBuf)
		if n > 0 {
			buf := r.getBuf()
			buf = stripper.Strip(buf[:0], readBuf[:n])
			if len(buf) > 0 {
				select {
				case rawCh <- buf:
				case <-ctx.Done():
					r.putBuf(buf)
					return ctx.Err()
				}
			} else {
				r.putBuf(buf)
			}
		}
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("stream ended (EOF)")
			}
			return fmt.Errorf("reading stream: %w", err)
		}
	}
}
