package creole

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempLexicon(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "lexicon.jsonl")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := writeTempLexicon(t, `{"creole": "marmay", "french": "enfant"}
{"creole": "péi", "french": "pays, île de La Réunion"}
{"creole": "granmoun", "french": "personne âgée"}
`)
	lex, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if lex.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", lex.Len())
	}
}

func TestLoad_SkipsCommentsAndBlanks(t *testing.T) {
	path := writeTempLexicon(t, `# This is a comment
{"creole": "marmay", "french": "enfant"}

{"creole": "péi", "french": "pays"}
`)
	lex, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if lex.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", lex.Len())
	}
}

func TestContextBiasWords(t *testing.T) {
	path := writeTempLexicon(t, `{"creole": "mi lé la", "french": "je suis là"}
{"creole": "kosa i lé", "french": "qu'est-ce que c'est"}
`)
	lex, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	words := lex.ContextBiasWords(100)
	// "mi" is skipped (len < 2), "lé" kept, "la" skipped, "kosa" kept, "i" skipped
	if len(words) < 2 {
		t.Fatalf("expected at least 2 words, got %d: %v", len(words), words)
	}
}

func TestContextBiasWords_MaxLimit(t *testing.T) {
	path := writeTempLexicon(t, `{"creole": "marmay", "french": "enfant"}
{"creole": "péi", "french": "pays"}
{"creole": "granmoun", "french": "personne âgée"}
`)
	lex, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	words := lex.ContextBiasWords(2)
	if len(words) != 2 {
		t.Fatalf("expected 2 words (max), got %d", len(words))
	}
}

func TestFewShotPrompt(t *testing.T) {
	path := writeTempLexicon(t, `{"creole": "marmay", "french": "enfant"}
{"creole": "péi", "french": "pays"}
`)
	lex, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	prompt := lex.FewShotPrompt()
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(prompt, "marmay") || !contains(prompt, "enfant") {
		t.Fatalf("prompt missing expected content: %s", prompt)
	}
}

func TestFewShotPrompt_Empty(t *testing.T) {
	lex := &Lexicon{}
	if prompt := lex.FewShotPrompt(); prompt != "" {
		t.Fatalf("expected empty prompt, got %q", prompt)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && containsStr(s, sub)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
