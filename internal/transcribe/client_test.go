package transcribe

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"freedom/internal/chunk"
)

func TestClient_TranscribeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("expected 'Bearer test-key', got %q", auth)
		}

		ct := r.Header.Get("Content-Type")
		if ct == "" {
			t.Error("missing Content-Type header")
		}

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("parsing multipart: %v", err)
		}

		model := r.FormValue("model")
		if model != "voxtral-mini-2602" {
			t.Errorf("model = %q, want 'voxtral-mini-2602'", model)
		}

		lang := r.FormValue("language")
		if lang != "fr" {
			t.Errorf("language = %q, want 'fr'", lang)
		}

		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("reading form file: %v", err)
		}
		file.Close()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResponse{Text: "bonjour le monde"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "test-key", "voxtral-mini-2602", "fr", nil)

	ch := chunk.Chunk{
		SeqNum:   0,
		RawMP3:   []byte("fake-mp3-data"),
		Duration: 10 * time.Second,
	}

	text, err := c.Transcribe(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "bonjour le monde" {
		t.Errorf("text = %q, want 'bonjour le monde'", text)
	}
}

func TestClient_TranscribeRetryOn500(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal error"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResponse{Text: "recovered"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "key", "voxtral-mini-2602", "fr", nil)

	ch := chunk.Chunk{
		SeqNum:   1,
		RawMP3:   []byte("data"),
		Duration: 5 * time.Second,
	}

	text, err := c.Transcribe(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "recovered" {
		t.Errorf("text = %q, want 'recovered'", text)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestClient_TranscribeRateLimit(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResponse{Text: "after rate limit"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "key", "voxtral-mini-2602", "fr", nil)

	ch := chunk.Chunk{
		SeqNum:   2,
		RawMP3:   []byte("data"),
		Duration: 5 * time.Second,
	}

	text, err := c.Transcribe(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "after rate limit" {
		t.Errorf("text = %q, want 'after rate limit'", text)
	}
}

func TestClient_TranscribeAllRetriesFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("permanent failure"))
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "key", "voxtral-mini-2602", "fr", nil)

	ch := chunk.Chunk{
		SeqNum:   3,
		RawMP3:   []byte("data"),
		Duration: 5 * time.Second,
	}

	_, err := c.Transcribe(context.Background(), ch)
	if err == nil {
		t.Fatal("expected error after all retries fail")
	}
}

func TestClient_TranscribeContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResponse{Text: "too late"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "key", "voxtral-mini-2602", "fr", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ch := chunk.Chunk{
		SeqNum:   4,
		RawMP3:   []byte("data"),
		Duration: 5 * time.Second,
	}

	_, err := c.Transcribe(ctx, ch)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestClient_ContextBiasFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("parsing multipart: %v", err)
		}

		biasValues := r.MultipartForm.Value["context_bias"]
		if len(biasValues) != 2 {
			t.Errorf("expected 2 context_bias values, got %d", len(biasValues))
		}
		if biasValues[0] != "liberté" || biasValues[1] != "égalité" {
			t.Errorf("context_bias = %v, want [liberté, égalité]", biasValues)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResponse{Text: "ok"})
	}))
	defer srv.Close()

	c := newTestClient(srv.URL, "key", "voxtral-mini-2602", "fr", []string{"liberté", "égalité"})

	ch := chunk.Chunk{
		SeqNum:   5,
		RawMP3:   []byte("data"),
		Duration: 5 * time.Second,
	}

	_, err := c.Transcribe(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{"0", 0},
		{"30", 30 * time.Second},
		{"invalid", 5 * time.Second},
	}
	for _, tt := range tests {
		got := parseRetryAfter(tt.input)
		if got != tt.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// newTestClient creates a Client pointing at the given test server URL.
func newTestClient(baseURL, apiKey, model, language string, contextBias []string) *Client {
	c := NewClient(apiKey, model, language, contextBias, slog.Default())
	// Override the HTTP client to use the test server.
	c.http = &http.Client{Timeout: 10 * time.Second}
	// We need to override the API URL for testing. We do this by wrapping doRequest.
	// Since the URL is a const, we make the client use a custom transport that rewrites the URL.
	c.http.Transport = &urlRewriter{base: baseURL}
	return c
}

// urlRewriter is an http.RoundTripper that rewrites the request URL to point at a test server.
type urlRewriter struct {
	base string
}

func (u *urlRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = u.base[len("http://"):]
	return http.DefaultTransport.RoundTrip(req)
}
