package mistral

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func TestComplete_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing or wrong auth header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing content-type")
		}

		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}

		if req.ResponseFormat != nil {
			t.Error("expected nil response_format for plain text")
		}
		if len(req.Messages) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(req.Messages))
		}
		if req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
			t.Error("unexpected message roles")
		}

		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "hello world"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewChatClient("test-key", "test-model", 0.3, 1000, testLogger())
	client.http = srv.Client()

	// Override the URL by replacing the transport.
	origURL := chatURL
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})
	_ = origURL

	result, err := client.Complete(context.Background(), "system msg", "user msg", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", result)
	}
}

func TestComplete_JSONObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}

		if req.ResponseFormat == nil {
			t.Fatal("expected response_format to be set")
		}
		if req.ResponseFormat.Type != "json_object" {
			t.Fatalf("expected type json_object, got %q", req.ResponseFormat.Type)
		}
		if req.ResponseFormat.JSONSchema != nil {
			t.Error("expected nil json_schema for json_object mode")
		}

		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: `{"key":"value"}`}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewChatClient("test-key", "test-model", 0.3, 1000, testLogger())
	client.http = srv.Client()
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	rf := &ResponseFormat{Type: "json_object"}
	result, err := client.Complete(context.Background(), "sys", "usr", rf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != `{"key":"value"}` {
		t.Fatalf("unexpected result: %s", result)
	}
}

func TestComplete_JSONSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}

		if req.ResponseFormat == nil {
			t.Fatal("expected response_format to be set")
		}
		if req.ResponseFormat.Type != "json_schema" {
			t.Fatalf("expected type json_schema, got %q", req.ResponseFormat.Type)
		}
		if req.ResponseFormat.JSONSchema == nil {
			t.Fatal("expected json_schema to be set")
		}
		if req.ResponseFormat.JSONSchema.Name != "test_schema" {
			t.Fatalf("expected schema name test_schema, got %q", req.ResponseFormat.JSONSchema.Name)
		}
		if !req.ResponseFormat.JSONSchema.Strict {
			t.Error("expected strict to be true")
		}

		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: `{"category":"speech","confidence":"high"}`}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewChatClient("test-key", "mistral-small-latest", 0.0, 500, testLogger())
	client.http = srv.Client()
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	schema := json.RawMessage(`{"type":"object","properties":{"category":{"type":"string"},"confidence":{"type":"string"}},"required":["category","confidence"]}`)
	rf := &ResponseFormat{
		Type: "json_schema",
		JSONSchema: &JSONSchema{
			Name:   "test_schema",
			Strict: true,
			Schema: schema,
		},
	}

	result, err := client.Complete(context.Background(), "classify", "some text", rf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if parsed["category"] != "speech" {
		t.Fatalf("expected category speech, got %q", parsed["category"])
	}
}

func TestComplete_RetryOn429(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}

		resp := chatResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "success after retry"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewChatClient("test-key", "test-model", 0.3, 1000, testLogger())
	client.http = srv.Client()
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	result, err := client.Complete(context.Background(), "sys", "usr", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "success after retry" {
		t.Fatalf("unexpected result: %s", result)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestComplete_RetryExhausted429(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	client := NewChatClient("test-key", "test-model", 0.3, 1000, testLogger())
	client.http = srv.Client()
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.Complete(ctx, "sys", "usr", nil)
	if err == nil {
		t.Fatal("expected error after exhausted 429 retries")
	}
	if !strings.Contains(err.Error(), "rate limited (429)") {
		t.Fatalf("expected error to contain 'rate limited (429)', got: %v", err)
	}
	if got := attempts.Load(); got != 6 {
		t.Fatalf("expected 6 attempts, got %d", got)
	}
}

func TestComplete_RetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server error"}`))
	}))
	defer srv.Close()

	client := NewChatClient("test-key", "test-model", 0.3, 1000, testLogger())
	client.http = srv.Client()
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := client.Complete(ctx, "sys", "usr", nil)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
}

func TestComplete_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	client := NewChatClient("test-key", "test-model", 0.3, 1000, testLogger())
	client.http = srv.Client()
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := client.Complete(ctx, "sys", "usr", nil)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"5", 5},
		{"abc", 5}, // fallback
	}
	for _, tt := range tests {
		got := parseRetryAfter(tt.input)
		want := tt.expected
		if want == 0 && got != 0 {
			t.Errorf("parseRetryAfter(%q) = %v, want 0", tt.input, got)
		}
		if want > 0 {
			expected := int(got.Seconds())
			if expected != want {
				t.Errorf("parseRetryAfter(%q) = %v seconds, want %d", tt.input, expected, want)
			}
		}
	}
}

// roundTripFunc is an adapter to allow the use of ordinary functions as http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
