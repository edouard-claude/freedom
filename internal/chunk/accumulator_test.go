package chunk

import (
	"context"
	"testing"
	"time"

	"freedom/internal/mp3"
)

// makeFrame creates a synthetic MP3 frame with a given duration and size.
func makeFrame(dur time.Duration, size int) mp3.Frame {
	return mp3.Frame{
		RawBytes:   make([]byte, size),
		Bitrate:    128,
		SampleRate: 44100,
		Duration:   dur,
		Samples:    1152,
	}
}

func TestAccumulator_EmitsChunkAtTarget(t *testing.T) {
	acc := NewAccumulator(500*time.Millisecond, 0)

	frameCh := make(chan mp3.Frame, 100)
	chunkCh := make(chan Chunk, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Each frame is ~26ms (1152/44100), so we need about 20 frames for 500ms.
	frameDur := 26 * time.Millisecond
	frameCount := 25 // ~650ms, enough for one chunk

	go func() {
		for range frameCount {
			frameCh <- makeFrame(frameDur, 417)
		}
		close(frameCh)
	}()

	done := make(chan error, 1)
	go func() {
		done <- acc.Run(ctx, frameCh, chunkCh)
	}()

	var chunks []Chunk
	for c := range chunkCh {
		chunks = append(chunks, c)
	}

	if err := <-done; err != nil {
		t.Fatalf("accumulator error: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	if chunks[0].SeqNum != 0 {
		t.Errorf("first chunk seqnum = %d, want 0", chunks[0].SeqNum)
	}

	if chunks[0].Duration < 500*time.Millisecond {
		t.Errorf("chunk duration = %v, want >= 500ms", chunks[0].Duration)
	}
}

func TestAccumulator_SequenceNumbers(t *testing.T) {
	acc := NewAccumulator(100*time.Millisecond, 0)

	frameCh := make(chan mp3.Frame, 200)
	chunkCh := make(chan Chunk, 20)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	frameDur := 26 * time.Millisecond
	// 50 frames ~ 1300ms, should produce multiple 100ms chunks
	go func() {
		for range 50 {
			frameCh <- makeFrame(frameDur, 417)
		}
		close(frameCh)
	}()

	done := make(chan error, 1)
	go func() {
		done <- acc.Run(ctx, frameCh, chunkCh)
	}()

	var chunks []Chunk
	for c := range chunkCh {
		chunks = append(chunks, c)
	}

	if err := <-done; err != nil {
		t.Fatalf("accumulator error: %v", err)
	}

	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}

	for i, c := range chunks {
		if c.SeqNum != uint64(i) {
			t.Errorf("chunk %d: seqnum = %d, want %d", i, c.SeqNum, i)
		}
	}
}

func TestAccumulator_RawMP3Size(t *testing.T) {
	acc := NewAccumulator(100*time.Millisecond, 0)

	frameCh := make(chan mp3.Frame, 100)
	chunkCh := make(chan Chunk, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	frameDur := 26 * time.Millisecond
	frameSize := 417
	frameCount := 10

	go func() {
		for range frameCount {
			frameCh <- makeFrame(frameDur, frameSize)
		}
		close(frameCh)
	}()

	done := make(chan error, 1)
	go func() {
		done <- acc.Run(ctx, frameCh, chunkCh)
	}()

	totalBytes := 0
	for c := range chunkCh {
		totalBytes += len(c.RawMP3)
	}

	if err := <-done; err != nil {
		t.Fatalf("accumulator error: %v", err)
	}

	// Without overlap, all frame bytes should appear exactly once.
	expected := frameCount * frameSize
	if totalBytes != expected {
		t.Errorf("total bytes = %d, want %d", totalBytes, expected)
	}
}

func TestAccumulator_WithOverlap(t *testing.T) {
	acc := NewAccumulator(200*time.Millisecond, 50*time.Millisecond)

	frameCh := make(chan mp3.Frame, 200)
	chunkCh := make(chan Chunk, 20)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	frameDur := 26 * time.Millisecond
	// 30 frames ~ 780ms, should produce several 200ms chunks
	go func() {
		for range 30 {
			frameCh <- makeFrame(frameDur, 417)
		}
		close(frameCh)
	}()

	done := make(chan error, 1)
	go func() {
		done <- acc.Run(ctx, frameCh, chunkCh)
	}()

	var chunks []Chunk
	for c := range chunkCh {
		chunks = append(chunks, c)
	}

	if err := <-done; err != nil {
		t.Fatalf("accumulator error: %v", err)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// With overlap, total bytes should exceed non-overlap total.
	totalBytes := 0
	for _, c := range chunks {
		totalBytes += len(c.RawMP3)
	}
	noOverlapTotal := 30 * 417
	if totalBytes <= noOverlapTotal {
		t.Errorf("with overlap, total bytes (%d) should exceed non-overlap total (%d)", totalBytes, noOverlapTotal)
	}
}

func TestAccumulator_ContextCancellation(t *testing.T) {
	acc := NewAccumulator(10*time.Second, 0)

	frameCh := make(chan mp3.Frame)
	chunkCh := make(chan Chunk, 10)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- acc.Run(ctx, frameCh, chunkCh)
	}()

	cancel()

	err := <-done
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestAccumulator_EmptyInput(t *testing.T) {
	acc := NewAccumulator(500*time.Millisecond, 0)

	frameCh := make(chan mp3.Frame)
	chunkCh := make(chan Chunk, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	close(frameCh)

	done := make(chan error, 1)
	go func() {
		done <- acc.Run(ctx, frameCh, chunkCh)
	}()

	var chunks []Chunk
	for c := range chunkCh {
		chunks = append(chunks, c)
	}

	if err := <-done; err != nil {
		t.Fatalf("accumulator error: %v", err)
	}

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty input, got %d", len(chunks))
	}
}
