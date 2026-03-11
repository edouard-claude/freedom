package web

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(nil, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSSEHub_RegisterUnregister(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Register a client.
	ch := make(chan sseMessage, 16)
	hub.register <- ch

	if n := hub.ClientCount(); n != 1 {
		t.Fatalf("expected 1 client, got %d", n)
	}

	// Unregister.
	hub.unregister <- ch

	if n := hub.ClientCount(); n != 0 {
		t.Fatalf("expected 0 clients after unregister, got %d", n)
	}
}

func TestSSEHub_Broadcast(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Register two clients.
	ch1 := make(chan sseMessage, 16)
	ch2 := make(chan sseMessage, 16)
	hub.register <- ch1
	hub.register <- ch2

	// Wait for registration to be processed.
	if n := hub.ClientCount(); n != 2 {
		t.Fatalf("expected 2 clients, got %d", n)
	}

	// Broadcast a message.
	hub.Notify("<article>test</article>")

	select {
	case msg := <-ch1:
		if msg.Event != "article" || msg.Data != "<article>test</article>" {
			t.Fatalf("ch1 got unexpected message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("ch1 timed out waiting for broadcast")
	}

	select {
	case msg := <-ch2:
		if msg.Event != "article" || msg.Data != "<article>test</article>" {
			t.Fatalf("ch2 got unexpected message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("ch2 timed out waiting for broadcast")
	}
}

func TestSSEHub_BroadcastTranscript(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	ch := make(chan sseMessage, 16)
	hub.register <- ch

	hub.ClientCount() // sync barrier

	hub.NotifyTranscript("<span>bonjour</span>")

	select {
	case msg := <-ch:
		if msg.Event != "transcript" || msg.Data != "<span>bonjour</span>" {
			t.Fatalf("got unexpected message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transcript broadcast")
	}
}

func TestSSEHub_ContextCancellation(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		hub.Run(ctx)
		close(done)
	}()

	// Register a client.
	ch := make(chan sseMessage, 16)
	hub.register <- ch

	hub.ClientCount() // sync barrier

	// Cancel context - hub should shut down and close client channels.
	cancel()

	select {
	case <-done:
		// Hub exited as expected.
	case <-time.After(time.Second):
		t.Fatal("hub did not exit after context cancellation")
	}

	// Client channel should be closed.
	_, ok := <-ch
	if ok {
		t.Fatal("expected client channel to be closed after hub shutdown")
	}
}

func TestSSEEndpoint(t *testing.T) {
	hub := NewSSEHub(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go hub.Run(ctx)

	// Create test server with SSE handler.
	ts := httptest.NewServer(hub)
	defer ts.Close()

	// Connect to SSE endpoint.
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("failed to connect to SSE endpoint: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Give the hub time to process the registration.
	time.Sleep(50 * time.Millisecond)

	// Send a notification.
	hub.Notify("<article>hello</article>")

	// Read the SSE event.
	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	deadline := time.After(2 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatalf("timed out reading SSE event, got lines: %v", lines)
		default:
		}

		if scanner.Scan() {
			line := scanner.Text()
			lines = append(lines, line)
			// SSE events end with an empty line.
			if line == "" && len(lines) > 1 {
				break
			}
		} else {
			break
		}
	}

	// Verify we got an "article" event with the correct data.
	foundEvent := false
	foundData := false
	for _, line := range lines {
		if line == "event: article" {
			foundEvent = true
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "hello") {
			foundData = true
		}
	}

	if !foundEvent {
		t.Errorf("expected 'event: article' in SSE output, got: %v", lines)
	}
	if !foundData {
		t.Errorf("expected data containing 'hello' in SSE output, got: %v", lines)
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"hello", []string{"hello"}},
		{"hello\nworld", []string{"hello", "world"}},
		{"a\nb\nc\n", []string{"a", "b", "c", ""}},
		{"", []string{""}},
		{"\n", []string{"", ""}},
		{"line1\r\nline2\r\n", []string{"line1", "line2", ""}},
	}

	for _, tt := range tests {
		got := splitLines(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("splitLines(%q): got %d lines, expected %d", tt.input, len(got), len(tt.expected))
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("splitLines(%q)[%d]: got %q, expected %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}

func TestHandleIndex_NoStore(t *testing.T) {
	// Test that the server can at least initialize templates without panicking.
	// Full handleIndex testing requires a storage.Client mock, which depends on
	// the storage package being available.
	logger := testLogger()
	hub := NewSSEHub(logger)

	// Verify template parsing works by creating server (will panic if templates are invalid).
	_ = NewServer(nil, hub, "0", nil, logger)
}
