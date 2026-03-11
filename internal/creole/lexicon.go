package creole

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Entry is a single creole-french translation pair.
type Entry struct {
	Creole string `json:"creole"`
	French string `json:"french"`
}

// Lexicon holds loaded creole-french pairs.
type Lexicon struct {
	entries []Entry
}

// Load reads a JSONL file of creole/french pairs.
// Each line must be: {"creole": "...", "french": "..."}
func Load(path string) (*Lexicon, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening lexicon file: %w", err)
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || text[0] == '#' {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(text), &e); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		if e.Creole != "" {
			entries = append(entries, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading lexicon file: %w", err)
	}
	return &Lexicon{entries: entries}, nil
}

// ContextBiasWords extracts unique creole words for Voxtral context_bias.
// The Voxtral API accepts up to 100 bias words to improve transcription
// of domain-specific vocabulary.
func (l *Lexicon) ContextBiasWords(max int) []string {
	if max <= 0 {
		max = 100
	}
	seen := make(map[string]struct{})
	var words []string
	for _, e := range l.entries {
		for _, w := range strings.Fields(e.Creole) {
			w = strings.ToLower(w)
			if len(w) < 2 {
				continue
			}
			if _, ok := seen[w]; ok {
				continue
			}
			seen[w] = struct{}{}
			words = append(words, w)
			if len(words) >= max {
				return words
			}
		}
	}
	return words
}

// FewShotPrompt formats entries as a creole-french lexicon for LLM prompts.
func (l *Lexicon) FewShotPrompt() string {
	if len(l.entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("LEXIQUE CRÉOLE RÉUNIONNAIS → FRANÇAIS :\n")
	for _, e := range l.entries {
		fmt.Fprintf(&sb, "- « %s » → « %s »\n", e.Creole, e.French)
	}
	return sb.String()
}

// Len returns the number of entries.
func (l *Lexicon) Len() int {
	return len(l.entries)
}
