package config

import (
	"flag"
	"fmt"
	"os"
)

// Config holds all runtime configuration.
type Config struct {
	// API
	MistralAPIKey string
	HFToken       string // Hugging Face token for image generation

	// Stream
	StreamURL     string  // Icecast stream URL
	ChunkDuration float64 // seconds
	Overlap       float64 // seconds
	Workers       int     // transcription workers
	Language      string  // transcription language

	// Models
	TranscribeModel string
	ClassifyModel   string
	ArticleModel    string
	ImageModel      string

	// Article
	ArticleWindow int    // segments per window
	LexiconFile   string // creole lexicon JSONL file

	// S3/MinIO
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Bucket    string
	S3UseSSL    bool

	// Web
	HTTPPort string

	// Schedule
	Schedule         string // "HH:MM-HH:MM" active window (empty = 24/7)
	ScheduleTimezone string // IANA timezone (default: Indian/Reunion)

	// Logging
	LogLevel string
}

// Parse reads configuration from flags and environment variables.
func Parse() (Config, error) {
	cfg := Config{}

	// Stream
	flag.StringVar(&cfg.StreamURL, "stream-url", "https://freedomice.streamakaci.com/freedom.mp3", "Icecast stream URL")
	flag.Float64Var(&cfg.ChunkDuration, "chunk-duration", 10, "chunk duration in seconds")
	flag.Float64Var(&cfg.Overlap, "overlap", 1.0, "overlap duration in seconds between chunks")
	flag.IntVar(&cfg.Workers, "workers", 3, "number of transcription workers")
	flag.StringVar(&cfg.Language, "language", "fr", "transcription language")

	// Models
	flag.StringVar(&cfg.TranscribeModel, "transcribe-model", "voxtral-mini-2602", "Voxtral transcription model")
	flag.StringVar(&cfg.ClassifyModel, "classify-model", "mistral-small-latest", "classification model")
	flag.StringVar(&cfg.ArticleModel, "article-model", "mistral-medium-latest", "article generation model")
	flag.StringVar(&cfg.ImageModel, "image-model", "mistral-medium-2505", "image generation model")

	// Article
	flag.IntVar(&cfg.ArticleWindow, "article-window", 12, "number of segments per article window")
	flag.StringVar(&cfg.LexiconFile, "lexicon", "", "creole lexicon JSONL file (empty = disabled)")

	// S3/MinIO
	flag.StringVar(&cfg.S3Endpoint, "s3-endpoint", "", "S3/MinIO endpoint")
	flag.StringVar(&cfg.S3AccessKey, "s3-access-key", "", "S3 access key")
	flag.StringVar(&cfg.S3SecretKey, "s3-secret-key", "", "S3 secret key")
	flag.StringVar(&cfg.S3Bucket, "s3-bucket", "freedom", "S3 bucket name")
	flag.BoolVar(&cfg.S3UseSSL, "s3-use-ssl", false, "use SSL for S3 connection")

	// Web
	flag.StringVar(&cfg.HTTPPort, "http-port", "8080", "HTTP server port")

	// Schedule
	flag.StringVar(&cfg.Schedule, "schedule", "", "active window HH:MM-HH:MM (empty = 24/7)")
	flag.StringVar(&cfg.ScheduleTimezone, "schedule-tz", "Indian/Reunion", "IANA timezone for schedule")

	// Logging
	flag.StringVar(&cfg.LogLevel, "log-level", "info", "log level (debug, info, warn, error)")

	flag.Parse()

	// API keys from environment.
	cfg.MistralAPIKey = os.Getenv("MISTRAL_API_KEY")
	if cfg.MistralAPIKey == "" {
		return cfg, fmt.Errorf("MISTRAL_API_KEY environment variable is required")
	}
	cfg.HFToken = os.Getenv("HF_TOKEN")

	// S3 credentials from environment if not set via flags.
	if cfg.S3Endpoint == "" {
		cfg.S3Endpoint = os.Getenv("S3_ENDPOINT")
	}
	if cfg.S3AccessKey == "" {
		cfg.S3AccessKey = os.Getenv("S3_ACCESS_KEY")
	}
	if cfg.S3SecretKey == "" {
		cfg.S3SecretKey = os.Getenv("S3_SECRET_KEY")
	}
	if v := os.Getenv("S3_BUCKET"); v != "" && cfg.S3Bucket == "freedom" {
		cfg.S3Bucket = v
	}
	if v := os.Getenv("S3_USE_SSL"); v == "true" || v == "1" {
		cfg.S3UseSSL = true
	}
	if cfg.Schedule == "" {
		cfg.Schedule = os.Getenv("SCHEDULE")
	}
	scheduleTZSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "schedule-tz" {
			scheduleTZSet = true
		}
	})
	if !scheduleTZSet {
		if v := os.Getenv("SCHEDULE_TZ"); v != "" {
			cfg.ScheduleTimezone = v
		}
	}

	// Validation.
	if cfg.ChunkDuration < 1 {
		return cfg, fmt.Errorf("chunk-duration must be >= 1 second")
	}
	if cfg.Workers < 1 {
		return cfg, fmt.Errorf("workers must be >= 1")
	}
	if cfg.S3Endpoint == "" {
		return cfg, fmt.Errorf("S3 endpoint is required (--s3-endpoint or S3_ENDPOINT)")
	}

	return cfg, nil
}
