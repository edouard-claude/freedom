package mistral

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

const (
	chatURL    = "https://api.mistral.ai/v1/chat/completions"
	maxRetries = 3
)

// ChatClient calls the Mistral Chat Completions API with support for
// json_object and json_schema response formats.
type ChatClient struct {
	apiKey      string
	model       string
	temperature float64
	maxTokens   int
	logger      *slog.Logger
	http        *http.Client
}

// NewChatClient creates a chat API client.
func NewChatClient(apiKey, model string, temperature float64, maxTokens int, logger *slog.Logger) *ChatClient {
	return &ChatClient{
		apiKey:      apiKey,
		model:       model,
		temperature: temperature,
		maxTokens:   maxTokens,
		logger:      logger,
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Message is a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat supports both json_object and json_schema types.
type ResponseFormat struct {
	Type       string      `json:"type"` // "json_object" or "json_schema"
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

// JSONSchema defines a strict schema for structured output.
type JSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Complete sends a chat completion request and returns the assistant response.
// Pass nil for rf to get plain text output, or provide a ResponseFormat for
// json_object / json_schema structured output.
func (c *ChatClient) Complete(ctx context.Context, system, user string, rf *ResponseFormat) (string, error) {
	messages := []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}

	var lastErr error
	for attempt := range maxRetries {
		text, retryAfter, err := c.doRequest(ctx, messages, rf)
		if err == nil {
			return text, nil
		}
		lastErr = err

		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		wait := retryAfter
		if wait == 0 {
			wait = time.Duration(1<<uint(attempt)) * time.Second
		}
		c.logger.Warn("chat request failed, retrying",
			"attempt", attempt+1, "error", err, "wait", wait)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("chat failed after %d retries: %w", maxRetries, lastErr)
}

func (c *ChatClient) doRequest(ctx context.Context, messages []Message, rf *ResponseFormat) (string, time.Duration, error) {
	reqBody := chatRequest{
		Model:          c.model,
		Messages:       messages,
		Temperature:    c.temperature,
		MaxTokens:      c.maxTokens,
		ResponseFormat: rf,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(data))
	if err != nil {
		return "", 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
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

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", 0, fmt.Errorf("parsing response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", 0, fmt.Errorf("no choices in response")
	}

	return chatResp.Choices[0].Message.Content, 0, nil
}

// SetHTTPClient replaces the underlying HTTP client (useful for testing).
func (c *ChatClient) SetHTTPClient(hc *http.Client) {
	c.http = hc
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
