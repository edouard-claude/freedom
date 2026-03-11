package mistral

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHfInferenceURL(t *testing.T) {
	if !strings.Contains(hfInferenceURL, "hf-inference") {
		t.Errorf("hfInferenceURL should use hf-inference provider, got: %s", hfInferenceURL)
	}
	if !strings.Contains(hfInferenceURL, "black-forest-labs/FLUX.1-schnell") {
		t.Errorf("hfInferenceURL should target FLUX.1-schnell model, got: %s", hfInferenceURL)
	}
}

func TestImageClient_Generate_RateLimited(t *testing.T) {
	calls := 0
	testPNG := createTestPNG(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(testPNG)
	}))
	defer srv.Close()

	ic := NewImageClient("test-token", testLogger())
	ic.http = srv.Client()
	ic.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	jpegData, err := ic.Generate(context.Background(), "test 429 retry")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(jpegData)); err != nil {
		t.Fatalf("result is not valid JPEG: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls (2 rate-limited + 1 success), got %d", calls)
	}
}

func TestImageClient_Generate(t *testing.T) {
	testPNG := createTestPNG(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth header")
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(testPNG)
	}))
	defer srv.Close()

	ic := NewImageClient("test-token", testLogger())
	ic.http = srv.Client()
	ic.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	jpegData, err := ic.Generate(context.Background(), "a beautiful sunset")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	_, err = jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("result is not valid JPEG: %v", err)
	}
}

func TestImageClient_Generate_JPEGResponse(t *testing.T) {
	testJPEG := createTestJPEG(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(testJPEG)
	}))
	defer srv.Close()

	ic := NewImageClient("test-token", testLogger())
	ic.http = srv.Client()
	ic.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	jpegData, err := ic.Generate(context.Background(), "test jpeg passthrough")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	_, err = jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("result is not valid JPEG: %v", err)
	}
}

func TestImageClient_Generate_NoToken(t *testing.T) {
	ic := NewImageClient("", testLogger())
	data, err := ic.Generate(context.Background(), "test")
	if err != nil {
		t.Fatalf("expected nil error with placeholder, got: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected placeholder data")
	}
}

func TestImageClient_Generate_FallbackPlaceholder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server down"}`))
	}))
	defer srv.Close()

	ic := NewImageClient("test-token", testLogger())
	ic.http = srv.Client()
	ic.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = srv.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(r)
	})

	jpegData, err := ic.Generate(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("expected nil error with placeholder fallback, got: %v", err)
	}
	if len(jpegData) == 0 {
		t.Fatal("expected placeholder data, got empty")
	}

	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("placeholder is not valid JPEG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 800 || bounds.Dy() != 400 {
		t.Fatalf("expected 800x400, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestPlaceholder(t *testing.T) {
	data := placeholder()
	if len(data) == 0 {
		t.Fatal("placeholder returned empty data")
	}

	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("placeholder is not valid JPEG: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() != 800 || bounds.Dy() != 400 {
		t.Fatalf("expected 800x400, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestToJPEG_PNG(t *testing.T) {
	testPNG := createTestPNG(t)
	jpegData, err := toJPEG(testPNG)
	if err != nil {
		t.Fatalf("toJPEG with PNG input failed: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("result is not valid JPEG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 100 || bounds.Dy() != 50 {
		t.Fatalf("expected 100x50, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestToJPEG_JPEG(t *testing.T) {
	testJPEG := createTestJPEG(t)
	jpegData, err := toJPEG(testJPEG)
	if err != nil {
		t.Fatalf("toJPEG with JPEG input failed: %v", err)
	}
	_, err = jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		t.Fatalf("result is not valid JPEG: %v", err)
	}
}

func TestToJPEG_InvalidData(t *testing.T) {
	_, err := toJPEG([]byte("not an image"))
	if err == nil {
		t.Fatal("expected error for invalid image data")
	}
}

func createTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for y := range 50 {
		for x := range 100 {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding test PNG: %v", err)
	}
	return buf.Bytes()
}

func createTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for y := range 50 {
		for x := range 100 {
			img.Set(x, y, color.RGBA{R: 0, G: 0, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encoding test JPEG: %v", err)
	}
	return buf.Bytes()
}
