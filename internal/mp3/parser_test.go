package mp3

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// buildFrame constructs a valid MPEG1 Layer III 128kbps 44100Hz MP3 frame.
// Frame size = 1152/8 * 128000/44100 = 417 bytes (no padding).
func buildFrame(t *testing.T, padding bool) []byte {
	t.Helper()

	// MPEG1 Layer III 128kbps 44100Hz
	// Byte 0: 0xFF (sync)
	// Byte 1: 0xFB = 1111_1011 (sync=111, version=11=MPEG1, layer=01=LayerIII, protection=1)
	// Byte 2: bitrate_index=9 (128kbps for MPEG1 L3), samplerate_index=0 (44100), padding, private
	//         1001_00P0 where P is padding bit
	// Byte 3: 0x00 (mode, mode_ext, copyright, original, emphasis)
	b2 := byte(0x90) // 1001_0000
	if padding {
		b2 = 0x92 // 1001_0010
	}

	hdr := [4]byte{0xFF, 0xFB, b2, 0x00}

	h, err := ParseHeader(hdr)
	if err != nil {
		t.Fatalf("building test frame header: %v", err)
	}

	frameSize := h.FrameSize()
	frame := make([]byte, frameSize)
	copy(frame[:4], hdr[:])
	return frame
}

func TestParseHeader_ValidMPEG1LayerIII(t *testing.T) {
	hdr := [4]byte{0xFF, 0xFB, 0x90, 0x00}
	h, err := ParseHeader(hdr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Version != MPEGVersion1 {
		t.Errorf("version = %d, want %d", h.Version, MPEGVersion1)
	}
	if h.Layer != LayerIII {
		t.Errorf("layer = %d, want %d", h.Layer, LayerIII)
	}
	if h.Bitrate != 128 {
		t.Errorf("bitrate = %d, want 128", h.Bitrate)
	}
	if h.SampleRate != 44100 {
		t.Errorf("samplerate = %d, want 44100", h.SampleRate)
	}
	if h.Padding {
		t.Error("expected no padding")
	}
	if h.Samples != 1152 {
		t.Errorf("samples = %d, want 1152", h.Samples)
	}
}

func TestParseHeader_InvalidSync(t *testing.T) {
	hdr := [4]byte{0x00, 0x00, 0x00, 0x00}
	_, err := ParseHeader(hdr)
	if err != ErrInvalidSync {
		t.Errorf("expected ErrInvalidSync, got %v", err)
	}
}

func TestParseHeader_ReservedVersion(t *testing.T) {
	// 0xFF 0xE9 = sync + version=01 (reserved)
	hdr := [4]byte{0xFF, 0xE9, 0x90, 0x00}
	_, err := ParseHeader(hdr)
	if err != ErrReservedVersion {
		t.Errorf("expected ErrReservedVersion, got %v", err)
	}
}

func TestFrameSize_MPEG1LayerIII_128kbps_44100(t *testing.T) {
	hdr := [4]byte{0xFF, 0xFB, 0x90, 0x00}
	h, err := ParseHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	// Expected: 1152/8 * 128000/44100 = 417.959... = 417 bytes
	got := h.FrameSize()
	if got != 417 {
		t.Errorf("frameSize = %d, want 417", got)
	}
}

func TestFrameSize_WithPadding(t *testing.T) {
	hdr := [4]byte{0xFF, 0xFB, 0x92, 0x00}
	h, err := ParseHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Padding {
		t.Fatal("expected padding")
	}
	got := h.FrameSize()
	if got != 418 {
		t.Errorf("frameSize = %d, want 418", got)
	}
}

func TestHeaderDuration(t *testing.T) {
	hdr := [4]byte{0xFF, 0xFB, 0x90, 0x00}
	h, err := ParseHeader(hdr)
	if err != nil {
		t.Fatal(err)
	}
	dur := h.Duration()
	expected := float64(1152) / float64(44100)
	if dur < expected-0.0001 || dur > expected+0.0001 {
		t.Errorf("duration = %f, want ~%f", dur, expected)
	}
}

func TestParser_SingleFrame(t *testing.T) {
	frame := buildFrame(t, false)

	p := NewParser(slog.Default())
	rawCh := make(chan []byte, 1)
	frameCh := make(chan Frame, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Feed the raw data with two consecutive frames for two-frame validation.
	twoFrames := append(frame, frame...)
	rawCh <- twoFrames
	close(rawCh)

	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx, rawCh, frameCh, func([]byte) {})
	}()

	var frames []Frame
	for f := range frameCh {
		frames = append(frames, f)
	}

	if err := <-done; err != nil {
		t.Fatalf("parser returned error: %v", err)
	}

	if len(frames) < 1 {
		t.Fatal("expected at least 1 frame, got 0")
	}

	f := frames[0]
	if f.Bitrate != 128 {
		t.Errorf("frame bitrate = %d, want 128", f.Bitrate)
	}
	if f.SampleRate != 44100 {
		t.Errorf("frame samplerate = %d, want 44100", f.SampleRate)
	}
	if len(f.RawBytes) != 417 {
		t.Errorf("frame size = %d, want 417", len(f.RawBytes))
	}
}

func TestParser_SkipsGarbage(t *testing.T) {
	frame := buildFrame(t, false)

	// Prepend garbage bytes.
	garbage := make([]byte, 100)
	for i := range garbage {
		garbage[i] = byte(i % 200)
	}
	data := append(garbage, frame...)
	data = append(data, frame...) // second frame for validation

	p := NewParser(slog.Default())
	rawCh := make(chan []byte, 1)
	frameCh := make(chan Frame, 10)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rawCh <- data
	close(rawCh)

	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx, rawCh, frameCh, func([]byte) {})
	}()

	var frames []Frame
	for f := range frameCh {
		frames = append(frames, f)
	}

	if err := <-done; err != nil {
		t.Fatalf("parser error: %v", err)
	}

	if len(frames) < 1 {
		t.Fatalf("expected at least 1 frame after garbage, got %d", len(frames))
	}
}

func TestParser_ContextCancellation(t *testing.T) {
	p := NewParser(slog.Default())
	rawCh := make(chan []byte)
	frameCh := make(chan Frame, 10)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx, rawCh, frameCh, func([]byte) {})
	}()

	cancel()

	err := <-done
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestParser_MultipleChunks(t *testing.T) {
	frame := buildFrame(t, false)

	// Send frames in separate raw chunks.
	p := NewParser(slog.Default())
	rawCh := make(chan []byte, 10)
	frameCh := make(chan Frame, 20)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Build 5 frames total sent across 2 raw chunks.
	chunk1 := make([]byte, 0)
	for range 3 {
		chunk1 = append(chunk1, frame...)
	}
	chunk2 := make([]byte, 0)
	for range 2 {
		chunk2 = append(chunk2, frame...)
	}

	rawCh <- chunk1
	rawCh <- chunk2
	close(rawCh)

	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx, rawCh, frameCh, func([]byte) {})
	}()

	var frames []Frame
	for f := range frameCh {
		frames = append(frames, f)
	}

	if err := <-done; err != nil {
		t.Fatalf("parser error: %v", err)
	}

	// We should get at least 4 frames (the last frame in the final chunk
	// may or may not pass two-frame validation depending on data availability).
	if len(frames) < 4 {
		t.Errorf("expected at least 4 frames, got %d", len(frames))
	}
}
