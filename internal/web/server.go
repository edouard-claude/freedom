package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"freedom/internal/schedule"
	"freedom/internal/storage"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// ArticleView is the template data for rendering a single article card.
type ArticleView struct {
	Title          string
	Summary        string
	Body           string
	Category       string // "Actualités" or "Entraide"
	CoverURL       string // /cover/{key} or empty
	AudioURL       string // /audio/{key}
	DetailURL      string // /article/{slug}
	ModelUsed      string
	CreatedAt      string // formatted for display
	CreatedAtISO   string // ISO 8601 for schema.org
	HasCover       bool
	IsListenerCall bool // true if Category == "Entraide"
}

// IndexData is the template data for the index page with pagination.
type IndexData struct {
	Articles     []ArticleView
	Page         int
	TotalPages   int
	HasPrev      bool
	HasNext      bool
	PrevPage     int
	NextPage     int
	Offline      bool   // true when outside schedule window
	NextStartAt  string // e.g. "06:00" — when pipeline resumes
	PipelineIdle bool   // true when in schedule window but pipeline not running
}

// ArticleDetailData is the template data for the article detail page.
type ArticleDetailData struct {
	Article  ArticleView
	Related  []ArticleView
	BaseURL  string
}

// Server serves the Freedom Radio AI web interface.
type Server struct {
	store           *storage.Client
	hub             *SSEHub
	logger          *slog.Logger
	tmpl            *template.Template
	httpPort        string
	sched           *schedule.Schedule
	pipelineRunning atomic.Bool
}

// NewServer creates a new web server wired to the given storage and SSE hub.
func NewServer(store *storage.Client, hub *SSEHub, httpPort string, sched *schedule.Schedule, logger *slog.Logger) *Server {
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))

	return &Server{
		store:    store,
		hub:      hub,
		logger:   logger,
		tmpl:     tmpl,
		httpPort: httpPort,
		sched:    sched,
	}
}

// SetPipelineRunning updates the pipeline running state.
func (s *Server) SetPipelineRunning(running bool) {
	s.pipelineRunning.Store(running)
}

// IsPipelineRunning reports whether the pipeline is currently running.
func (s *Server) IsPipelineRunning() bool {
	return s.pipelineRunning.Load()
}

// Run starts the HTTP server and blocks until ctx is cancelled,
// then performs a graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()

	// Main page.
	mux.HandleFunc("GET /", s.handleIndex)

	// SSE endpoint for real-time article updates.
	mux.Handle("GET /sse", s.hub)

	// Embedded static files (CSS).
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Article detail page.
	mux.HandleFunc("GET /article/", s.handleArticleDetail)

	// Sitemap for SEO / Google News.
	mux.HandleFunc("GET /sitemap.xml", s.handleSitemap)

	// Proxy audio from S3.
	mux.HandleFunc("GET /audio/", s.handleAudio)

	// Proxy cover images from S3.
	mux.HandleFunc("GET /cover/", s.handleCover)

	srv := &http.Server{
		Addr:              ":" + s.httpPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Start serving in background.
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("web server starting", "port", s.httpPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for context cancellation or server error.
	select {
	case <-ctx.Done():
		s.logger.Info("web server shutting down")
	case err := <-errCh:
		return err
	}

	// Graceful shutdown with 10s deadline.
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}

// RenderArticleCard renders a single article card HTML fragment.
// Used by the SSE hub to send new articles to connected clients.
func (s *Server) RenderArticleCard(a storage.Article) (string, error) {
	view := articleToView(a)
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "article-card", view); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ListenForArticles reads storage.Article values from articleCh, renders each
// one as an HTML card fragment, and broadcasts it to all connected SSE clients.
// It blocks until ctx is cancelled or articleCh is closed.
func (s *Server) ListenForArticles(ctx context.Context, articleCh <-chan storage.Article) {
	for {
		select {
		case <-ctx.Done():
			return
		case a, ok := <-articleCh:
			if !ok {
				return
			}
			html, err := s.RenderArticleCard(a)
			if err != nil {
				s.logger.Error("render article card for SSE", "title", a.Title, "error", err)
				continue
			}
			s.hub.Notify(html)
		}
	}
}

// articleSlug derives a URL slug from an article S3 key.
// Key format: "yy/mm/dd/hh-mm-ss-article.md" → slug: "yy/mm/dd/hh-mm-ss"
func articleSlug(key string) string {
	return strings.TrimSuffix(key, "-article.md")
}

// articleToView converts a storage Article to a template-friendly ArticleView.
func articleToView(a storage.Article) ArticleView {
	slug := articleSlug(a.Key)
	v := ArticleView{
		Title:          a.Title,
		Summary:        a.Summary,
		Body:           a.Body,
		Category:       a.Category,
		AudioURL:       "/audio/" + a.AudioKey,
		DetailURL:      "/article/" + slug,
		ModelUsed:      a.ModelUsed,
		CreatedAt:      a.CreatedAt.Format("02/01/2006 15:04"),
		CreatedAtISO:   a.CreatedAt.Format(time.RFC3339),
		IsListenerCall: a.Category == "Entraide",
	}
	if a.CoverKey != "" {
		v.CoverURL = "/cover/" + a.CoverKey
		v.HasCover = true
	}
	return v
}
