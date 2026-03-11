package article

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"freedom/internal/storage"
)

var phoneRegex = regexp.MustCompile(`\b0[67]\s?\d{2}\s?\d{2}\s?\d{2}\s?\d{2}\b`)

// Writer receives articles, anonymizes phone numbers, stores them, and
// notifies the SSE channel.
type Writer struct {
	store   *storage.Client
	logger  *slog.Logger
	sseCh   chan<- storage.Article
}

// NewWriter creates an article writer.
func NewWriter(store *storage.Client, logger *slog.Logger, sseCh chan<- storage.Article) *Writer {
	return &Writer{
		store:  store,
		logger: logger,
		sseCh:  sseCh,
	}
}

// Run reads articles from articleCh, anonymizes phone numbers, stores them,
// and notifies the SSE channel.
func (w *Writer) Run(ctx context.Context, articleCh <-chan Article) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case art, ok := <-articleCh:
			if !ok {
				return nil
			}

			// Anonymize phone numbers in body and summary.
			art.Body = phoneRegex.ReplaceAllString(art.Body, "[numero masque]")
			art.Summary = phoneRegex.ReplaceAllString(art.Summary, "[numero masque]")

			ts := art.CreatedAt.Format("15:04:05")

			// Print to stdout.
			fmt.Printf("\n=== ARTICLE [%s] ===\n", ts)
			fmt.Printf("Titre: %s\n", art.Title)
			fmt.Println("---")
			fmt.Println(art.Body)
			fmt.Println("========================")

			// Store in S3.
			sa := storage.Article{
				Key:       art.Key,
				Title:     art.Title,
				Category:  art.Category,
				Body:      art.Body,
				Summary:   art.Summary,
				AudioKey:  art.AudioKey,
				CoverKey:  art.CoverKey,
				CreatedAt: art.CreatedAt,
			}
			if err := w.store.PutArticle(ctx, sa); err != nil {
				w.logger.Error("storing article", "key", art.Key, "error", err)
			}

			// Notify SSE channel (non-blocking).
			if w.sseCh != nil {
				select {
				case w.sseCh <- sa:
				default:
					w.logger.Warn("SSE channel full, dropping notification", "key", art.Key)
				}
			}
		}
	}
}

// stripAccents removes diacritical marks from unicode characters.
func stripAccents(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
