package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"freedom/internal/mistral"
)

var classificationSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"category": {"type": "string", "enum": ["speech", "music", "advertisement", "silence", "mixed"]},
		"confidence": {"type": "string", "enum": ["high", "medium", "low"]}
	},
	"required": ["category", "confidence"],
	"additionalProperties": false
}`)

const systemPrompt = `You are a radio content classifier. Analyze the provided transcript and classify the content into exactly one category.

Categories:
- "speech": spoken word content such as news, interviews, or commentary
- "music": music is playing, lyrics, or musical performance
- "advertisement": commercial or promotional content
- "silence": no meaningful content, dead air, or very minimal sound
- "mixed": a combination of multiple categories with no dominant one

Also rate your confidence as "high", "medium", or "low".

Respond with the classification in the required JSON format.`

// Result holds the classification output.
type Result struct {
	Category   string `json:"category"`   // "speech", "music", "advertisement", "silence", "mixed"
	Confidence string `json:"confidence"` // "high", "medium", "low"
}

// Classifier uses mistral-small to classify transcribed radio content.
type Classifier struct {
	chat   *mistral.ChatClient
	logger *slog.Logger
}

// NewClassifier creates a classifier backed by the given ChatClient.
// The ChatClient should be configured with mistral-small-latest and temperature 0.
func NewClassifier(chat *mistral.ChatClient, logger *slog.Logger) *Classifier {
	return &Classifier{
		chat:   chat,
		logger: logger,
	}
}

// Classify analyzes a transcript and returns the classification result.
func (c *Classifier) Classify(ctx context.Context, transcript string) (Result, error) {
	rf := &mistral.ResponseFormat{
		Type: "json_schema",
		JSONSchema: &mistral.JSONSchema{
			Name:   "classification",
			Strict: true,
			Schema: classificationSchema,
		},
	}

	raw, err := c.chat.Complete(ctx, systemPrompt, transcript, rf)
	if err != nil {
		return Result{}, fmt.Errorf("classification request: %w", err)
	}

	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return Result{}, fmt.Errorf("parsing classification response %q: %w", raw, err)
	}

	if !validCategory(result.Category) {
		return Result{}, fmt.Errorf("invalid category %q", result.Category)
	}
	if !validConfidence(result.Confidence) {
		return Result{}, fmt.Errorf("invalid confidence %q", result.Confidence)
	}

	c.logger.Info("classified content",
		"category", result.Category,
		"confidence", result.Confidence)

	return result, nil
}

func validCategory(s string) bool {
	switch s {
	case "speech", "music", "advertisement", "silence", "mixed":
		return true
	}
	return false
}

func validConfidence(s string) bool {
	switch s {
	case "high", "medium", "low":
		return true
	}
	return false
}
