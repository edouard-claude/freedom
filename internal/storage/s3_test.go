package storage

import (
	"testing"
	"time"
)

func TestMarshalUnmarshalArticle(t *testing.T) {
	created := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	updated := time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)

	original := Article{
		Key:       "26/03/10/14-30-00-article.md",
		Title:     "Test Article Title",
		Category:  "speech",
		Summary:   "A brief summary of the article.",
		Body:      "This is the body of the article.\n\nIt has multiple paragraphs.",
		ModelUsed: "mistral-large-latest",
		CreatedAt: created,
		UpdatedAt: updated,
		AudioKey:  "26/03/10/14-30-00-sample.mp3",
		CoverKey:  "26/03/10/14-30-00-cover.jpg",
		Topics:    []string{"politics", "economy"},
		Status:    "published",
	}

	md := marshalArticle(original)

	parsed, err := unmarshalArticle(md)
	if err != nil {
		t.Fatalf("unmarshalArticle failed: %v", err)
	}

	if parsed.Title != original.Title {
		t.Errorf("Title: got %q, want %q", parsed.Title, original.Title)
	}
	if parsed.Category != original.Category {
		t.Errorf("Category: got %q, want %q", parsed.Category, original.Category)
	}
	if parsed.Summary != original.Summary {
		t.Errorf("Summary: got %q, want %q", parsed.Summary, original.Summary)
	}
	if parsed.Body != original.Body {
		t.Errorf("Body: got %q, want %q", parsed.Body, original.Body)
	}
	if parsed.ModelUsed != original.ModelUsed {
		t.Errorf("ModelUsed: got %q, want %q", parsed.ModelUsed, original.ModelUsed)
	}
	if !parsed.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", parsed.CreatedAt, original.CreatedAt)
	}
	if !parsed.UpdatedAt.Equal(original.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v, want %v", parsed.UpdatedAt, original.UpdatedAt)
	}
	if parsed.AudioKey != original.AudioKey {
		t.Errorf("AudioKey: got %q, want %q", parsed.AudioKey, original.AudioKey)
	}
	if parsed.CoverKey != original.CoverKey {
		t.Errorf("CoverKey: got %q, want %q", parsed.CoverKey, original.CoverKey)
	}
	if len(parsed.Topics) != len(original.Topics) {
		t.Fatalf("Topics length: got %d, want %d", len(parsed.Topics), len(original.Topics))
	}
	for i, topic := range parsed.Topics {
		if topic != original.Topics[i] {
			t.Errorf("Topics[%d]: got %q, want %q", i, topic, original.Topics[i])
		}
	}
	if parsed.Status != original.Status {
		t.Errorf("Status: got %q, want %q", parsed.Status, original.Status)
	}
}

func TestMarshalArticle_EmptyUpdatedAt(t *testing.T) {
	a := Article{
		Title:     "No Update",
		Category:  "music",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Topics:    []string{"jazz"},
		Status:    "draft",
	}

	md := marshalArticle(a)
	parsed, err := unmarshalArticle(md)
	if err != nil {
		t.Fatalf("unmarshalArticle failed: %v", err)
	}
	if !parsed.UpdatedAt.IsZero() {
		t.Errorf("expected zero UpdatedAt, got %v", parsed.UpdatedAt)
	}
}

func TestMarshalArticle_EmptyTopics(t *testing.T) {
	a := Article{
		Title:     "No Topics",
		Category:  "silence",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Topics:    nil,
		Status:    "published",
	}

	md := marshalArticle(a)
	parsed, err := unmarshalArticle(md)
	if err != nil {
		t.Fatalf("unmarshalArticle failed: %v", err)
	}
	if len(parsed.Topics) != 0 {
		t.Errorf("expected empty topics, got %v", parsed.Topics)
	}
}

func TestUnmarshalArticle_MissingFrontmatter(t *testing.T) {
	_, err := unmarshalArticle("no frontmatter here")
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestUnmarshalArticle_MissingClosingDelimiter(t *testing.T) {
	_, err := unmarshalArticle("---\ntitle: \"test\"\n")
	if err == nil {
		t.Fatal("expected error for missing closing delimiter")
	}
}

func TestUnquote(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{`"hello"`, "hello"},
		{`"with \"quotes\""`, `with "quotes"`},
		{"noquotes", "noquotes"},
		{`""`, ""},
	}
	for _, tt := range tests {
		got := unquote(tt.input)
		if got != tt.want {
			t.Errorf("unquote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseTopics(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{`["politics", "economy"]`, []string{"politics", "economy"}},
		{`["single"]`, []string{"single"}},
		{`[]`, nil},
		{``, nil},
	}
	for _, tt := range tests {
		got := parseTopics(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("parseTopics(%q) length = %d, want %d", tt.input, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseTopics(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestMarshalArticle_SpecialCharactersInTitle(t *testing.T) {
	a := Article{
		Title:     `Article with "quotes" and special chars: <>&`,
		Category:  "speech",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status:    "published",
	}

	md := marshalArticle(a)
	parsed, err := unmarshalArticle(md)
	if err != nil {
		t.Fatalf("unmarshalArticle failed: %v", err)
	}
	if parsed.Title != a.Title {
		t.Errorf("Title: got %q, want %q", parsed.Title, a.Title)
	}
}
