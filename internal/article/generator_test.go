package article

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"freedom/internal/mistral"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// newTestChatClient creates a ChatClient backed by the given httptest.Server.
func newTestChatClient(srv *httptest.Server) *mistral.ChatClient {
	c := mistral.NewChatClient("test-key", "test-model", 0, 512, testLogger())
	c.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = "http"
			r.URL.Host = srv.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(r)
		}),
	})
	return c
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// chatJSON returns an httptest handler that always responds with the given JSON content.
func chatJSON(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
		})
	}
}

func makeSegments(n int) []Segment {
	segs := make([]Segment, n)
	for i := range segs {
		segs[i] = Segment{
			Text:   fmt.Sprintf("segment %d text", i),
			RawMP3: []byte(fmt.Sprintf("mp3-%d", i)),
		}
	}
	return segs
}

func concatMP3(segs []Segment) []byte {
	var out []byte
	for _, s := range segs {
		out = append(out, s.RawMP3...)
	}
	return out
}

func TestTrimAudio_ValidRange(t *testing.T) {
	srv := httptest.NewServer(chatJSON(`{"start_index":2,"end_index":4}`))
	defer srv.Close()

	segs := makeSegments(8)
	g := &Generator{
		trimChat: newTestChatClient(srv),
		logger:   testLogger(),
	}
	window := Window{
		Text:     "full text",
		RawMP3:   concatMP3(segs),
		Segments: segs,
	}
	artResp := articleResponse{Title: "Test", Summary: "Test summary"}

	got := g.trimAudio(context.Background(), window, artResp)
	want := concatMP3(segs[2:5]) // end_index inclusive
	if string(got) != string(want) {
		t.Fatalf("trimAudio() = %q, want %q", got, want)
	}
}

func TestTrimAudio_FullRange(t *testing.T) {
	srv := httptest.NewServer(chatJSON(`{"start_index":0,"end_index":7}`))
	defer srv.Close()

	segs := makeSegments(8)
	fullMP3 := concatMP3(segs)
	// Use a distinct RawMP3 to verify trimAudio always concatenates from Segments.
	g := &Generator{
		trimChat: newTestChatClient(srv),
		logger:   testLogger(),
	}
	window := Window{Text: "full", RawMP3: []byte("different-raw"), Segments: segs}

	got := g.trimAudio(context.Background(), window, articleResponse{Title: "T", Summary: "S"})
	if string(got) != string(fullMP3) {
		t.Fatalf("expected concatenation from segments, got %q", got)
	}
}

func TestTrimAudio_InvalidRange(t *testing.T) {
	tests := []struct {
		name string
		resp string
	}{
		{"start > end", `{"start_index":5,"end_index":2}`},
		{"end out of bounds", `{"start_index":0,"end_index":99}`},
		{"negative start", `{"start_index":-1,"end_index":3}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(chatJSON(tt.resp))
			defer srv.Close()

			segs := makeSegments(8)
			fullMP3 := concatMP3(segs)
			g := &Generator{
				trimChat: newTestChatClient(srv),
				logger:   testLogger(),
			}
			window := Window{Text: "full", RawMP3: fullMP3, Segments: segs}

			got := g.trimAudio(context.Background(), window, articleResponse{Title: "T", Summary: "S"})
			if string(got) != string(fullMP3) {
				t.Fatal("expected fallback to full audio for invalid range")
			}
		})
	}
}

func TestTrimAudio_LLMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal"}`))
	}))
	defer srv.Close()

	segs := makeSegments(6)
	fullMP3 := concatMP3(segs)
	g := &Generator{
		trimChat: newTestChatClient(srv),
		logger:   testLogger(),
	}
	window := Window{Text: "full", RawMP3: fullMP3, Segments: segs}

	got := g.trimAudio(context.Background(), window, articleResponse{Title: "T", Summary: "S"})
	if string(got) != string(fullMP3) {
		t.Fatal("expected fallback to full audio on LLM error")
	}
}

func TestTrimAudio_ParseError(t *testing.T) {
	srv := httptest.NewServer(chatJSON(`not json`))
	defer srv.Close()

	segs := makeSegments(4)
	fullMP3 := concatMP3(segs)
	g := &Generator{
		trimChat: newTestChatClient(srv),
		logger:   testLogger(),
	}
	window := Window{Text: "full", RawMP3: fullMP3, Segments: segs}

	got := g.trimAudio(context.Background(), window, articleResponse{Title: "T", Summary: "S"})
	if string(got) != string(fullMP3) {
		t.Fatal("expected fallback to full audio on parse error")
	}
}

func TestTrimAudio_NilTrimChat(t *testing.T) {
	g := &Generator{trimChat: nil, logger: testLogger()}
	segs := makeSegments(4)
	fullMP3 := concatMP3(segs)
	window := Window{Text: "full", RawMP3: fullMP3, Segments: segs}

	got := g.trimAudio(context.Background(), window, articleResponse{Title: "T", Summary: "S"})
	if string(got) != string(fullMP3) {
		t.Fatal("expected fallback to full audio when trimChat is nil")
	}
}

func TestTrimAudio_EmptySegments(t *testing.T) {
	g := &Generator{logger: testLogger()}
	fullMP3 := []byte("full-audio")
	window := Window{Text: "full", RawMP3: fullMP3, Segments: nil}

	got := g.trimAudio(context.Background(), window, articleResponse{Title: "T", Summary: "S"})
	if string(got) != string(fullMP3) {
		t.Fatal("expected full audio when segments are empty")
	}
}
