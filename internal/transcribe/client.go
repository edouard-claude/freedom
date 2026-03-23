package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"freedom/internal/chunk"
)

const (
	apiURL     = "https://api.mistral.ai/v1/audio/transcriptions"
	maxRetries = 6
)

// Client calls the Voxtral transcription API.
type Client struct {
	apiKey      string
	model       string
	language    string
	contextBias []string
	logger      *slog.Logger
	http        *http.Client
}

// NewClient creates a transcription API client.
func NewClient(apiKey, model, language string, contextBias []string, logger *slog.Logger) *Client {
	return &Client{
		apiKey:      apiKey,
		model:       model,
		language:    language,
		contextBias: contextBias,
		logger:      logger,
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// SetHTTPClient replaces the underlying HTTP client (useful for testing and shared transport).
func (c *Client) SetHTTPClient(hc *http.Client) {
	c.http = hc
}

type apiResponse struct {
	Text string `json:"text"`
}

// Transcribe sends a chunk to the API and returns the transcription text.
func (c *Client) Transcribe(ctx context.Context, ch chunk.Chunk) (string, error) {
	var lastErr error

	for attempt := range maxRetries {
		text, retryAfter, err := c.doRequest(ctx, ch)
		if err == nil {
			return text, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		// Determine wait time: prefer Retry-After, otherwise exponential backoff
		// starting at 5s (5, 10, 20, 40, 80, 160).
		wait := retryAfter
		if wait == 0 {
			wait = time.Duration(5<<uint(attempt)) * time.Second
		}
		c.logger.Warn("transcription request failed, retrying",
			"attempt", attempt+1, "error", err, "wait", wait, "seq", ch.SeqNum)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("transcription failed after %d retries: %w", maxRetries, lastErr)
}

func (c *Client) doRequest(ctx context.Context, ch chunk.Chunk) (string, time.Duration, error) {
	// Build multipart body.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	if err := w.WriteField("model", c.model); err != nil {
		return "", 0, fmt.Errorf("writing model field: %w", err)
	}
	if err := w.WriteField("language", c.language); err != nil {
		return "", 0, fmt.Errorf("writing language field: %w", err)
	}
	for _, word := range c.contextBias {
		if err := w.WriteField("context_bias", word); err != nil {
			return "", 0, fmt.Errorf("writing context_bias field: %w", err)
		}
	}

	part, err := w.CreateFormFile("file", "chunk.mp3")
	if err != nil {
		return "", 0, fmt.Errorf("creating form file: %w", err)
	}
	if _, err := part.Write(ch.RawMP3); err != nil {
		return "", 0, fmt.Errorf("writing mp3 data: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", 0, fmt.Errorf("closing multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, &body)
	if err != nil {
		return "", 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return "", retryAfter, fmt.Errorf("rate limited (429)")
	}
	if resp.StatusCode >= 500 {
		return "", 0, fmt.Errorf("server error (%d): %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp apiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", 0, fmt.Errorf("parsing response: %w", err)
	}

	return apiResp.Text, 0, nil
}

func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 0
	}
	secs, err := strconv.Atoi(s)
	if err != nil {
		return 5 * time.Second
	}
	return time.Duration(secs) * time.Second
}
