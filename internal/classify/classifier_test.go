package classify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"log/slog"

	"freedom/internal/mistral"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// roundTripFunc is an adapter to allow the use of ordinary functions as http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestClassify_Speech(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages       []mistral.Message        `json:"messages"`
			ResponseFormat *mistral.ResponseFormat   `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}

		// Verify json_schema format was requested.
		if req.ResponseFormat == nil {
			t.Fatal("expected response_format to be set")
		}
		if req.ResponseFormat.Type != "json_schema" {
			t.Fatalf("expected type json_schema, got %q", req.ResponseFormat.Type)
		}
		if req.ResponseFormat.JSONSchema == nil {
			t.Fatal("expected json_schema to be set")
		}
		if req.ResponseFormat.JSONSchema.Name != "classification" {
			t.Fatalf("expected schema name classification, got %q", req.ResponseFormat.JSONSchema.Name)
		}
		if !req.ResponseFormat.JSONSchema.Strict {
			t.Error("expected strict to be true")
		}

		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": `{"category":"speech","confidence":"high"}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	chat := mistral.NewChatClient("test-key", "mistral-small-latest", 0.0, 500, testLogger())
	chat.SetHTTPClient(newRedirectClient(srv))

	c := NewClassifier(chat, testLogger())
	result, err := c.Classify(context.Background(), "Today in the news, the president announced new policy changes...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Category != "speech" {
		t.Fatalf("expected category speech, got %q", result.Category)
	}
	if result.Confidence != "high" {
		t.Fatalf("expected confidence high, got %q", result.Confidence)
	}
}

func TestClassify_Music(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": `{"category":"music","confidence":"medium"}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	chat := mistral.NewChatClient("test-key", "mistral-small-latest", 0.0, 500, testLogger())
	chat.SetHTTPClient(newRedirectClient(srv))

	c := NewClassifier(chat, testLogger())
	result, err := c.Classify(context.Background(), "la la la, singing in the rain...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Category != "music" {
		t.Fatalf("expected category music, got %q", result.Category)
	}
}

func TestClassify_InvalidCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": `{"category":"unknown","confidence":"high"}`,
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	chat := mistral.NewChatClient("test-key", "mistral-small-latest", 0.0, 500, testLogger())
	chat.SetHTTPClient(newRedirectClient(srv))

	c := NewClassifier(chat, testLogger())
	_, err := c.Classify(context.Background(), "test transcript")
	if err == nil {
		t.Fatal("expected error for invalid category")
	}
}

func TestClassify_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "not json at all",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	chat := mistral.NewChatClient("test-key", "mistral-small-latest", 0.0, 500, testLogger())
	chat.SetHTTPClient(newRedirectClient(srv))

	c := NewClassifier(chat, testLogger())
	_, err := c.Classify(context.Background(), "test transcript")
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestValidCategory(t *testing.T) {
	valid := []string{"speech", "music", "advertisement", "silence", "mixed"}
	for _, v := range valid {
		if !validCategory(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if validCategory("unknown") {
		t.Error("expected 'unknown' to be invalid")
	}
}

func TestValidConfidence(t *testing.T) {
	valid := []string{"high", "medium", "low"}
	for _, v := range valid {
		if !validConfidence(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if validConfidence("very_high") {
		t.Error("expected 'very_high' to be invalid")
	}
}

func newRedirectClient(srv *httptest.Server) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = "http"
			r.URL.Host = srv.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(r)
		}),
	}
}
