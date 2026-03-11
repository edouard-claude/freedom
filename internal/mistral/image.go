package mistral

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	hfInferenceURL = "https://router.huggingface.co/hf-inference/models/black-forest-labs/FLUX.1-schnell"
	jpegQuality    = 85
)

// ImageClient generates images via the Hugging Face Inference API (FLUX.1-schnell).
type ImageClient struct {
	hfToken string
	logger  *slog.Logger
	http    *http.Client
}

// NewImageClient creates an image generation client using HF Inference API.
func NewImageClient(hfToken string, logger *slog.Logger) *ImageClient {
	return &ImageClient{
		hfToken: hfToken,
		logger:  logger,
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Generate creates an image from the given prompt and returns JPEG bytes.
// On failure it returns a placeholder image.
func (ic *ImageClient) Generate(ctx context.Context, prompt string) ([]byte, error) {
	if ic.hfToken == "" {
		ic.logger.Warn("HF_TOKEN not set, returning placeholder")
		return placeholder(), nil
	}

	var lastErr error
	for attempt := range 3 {
		jpegData, retryAfter, err := ic.doGenerate(ctx, prompt)
		if err == nil {
			return jpegData, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			ic.logger.Error("image generation cancelled", "error", lastErr)
			return placeholder(), nil
		}

		if attempt == 2 {
			break
		}

		wait := retryAfter
		if wait == 0 {
			wait = time.Duration(2<<uint(attempt)) * time.Second
		}
		ic.logger.Warn("image generation failed, retrying",
			"attempt", attempt+1, "error", err, "wait", wait)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			ic.logger.Error("image generation cancelled", "error", lastErr)
			return placeholder(), nil
		}
	}

	ic.logger.Error("image generation failed after retries, returning placeholder", "error", lastErr)
	return placeholder(), nil
}

func (ic *ImageClient) doGenerate(ctx context.Context, prompt string) ([]byte, time.Duration, error) {
	body := fmt.Sprintf(`{"inputs":%q}`, prompt)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hfInferenceURL, strings.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ic.hfToken)

	resp, err := ic.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		ic.logger.Warn("reading response body", "error", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, retryAfter, fmt.Errorf("rate limited (429)")
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, 0, fmt.Errorf("model loading (503): %s", string(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("inference failed (%d): %s", resp.StatusCode, string(respBody))
	}

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "image/") {
		data, err := toJPEG(respBody)
		return data, 0, err
	}

	return nil, 0, fmt.Errorf("unexpected content-type %q: %s", ct, string(respBody[:min(200, len(respBody))]))
}

// toJPEG converts image bytes (JPEG or PNG) to JPEG at the configured quality.
func toJPEG(imgData []byte) ([]byte, error) {
	var img image.Image
	var err error

	img, err = jpeg.Decode(bytes.NewReader(imgData))
	if err != nil {
		img, err = png.Decode(bytes.NewReader(imgData))
		if err != nil {
			return nil, fmt.Errorf("decoding image (tried JPEG and PNG): %w", err)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("encoding JPEG: %w", err)
	}
	return buf.Bytes(), nil
}

// placeholder returns a simple 800x400 gray JPEG image.
func placeholder() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 800, 400))
	gray := color.RGBA{R: 128, G: 128, B: 128, A: 255}
	for y := range 400 {
		for x := range 800 {
			img.Set(x, y, gray)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil
	}
	return buf.Bytes()
}
