package article

import "time"

// Article represents a generated news article.
type Article struct {
	Key         string    `json:"key"`
	Title       string    `json:"title"`
	Category    string    `json:"category"`
	Body        string    `json:"body"`
	Summary     string    `json:"summary"`
	CoverPrompt string   `json:"cover_prompt"`
	Topics      []string  `json:"topics,omitempty"`
	ModelUsed   string    `json:"model_used"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	AudioKey    string    `json:"audio_key,omitempty"`
	CoverKey    string    `json:"cover_key,omitempty"`
	Status      string    `json:"status"`
	RawMP3      []byte    `json:"-"` // transient, not serialized
}
