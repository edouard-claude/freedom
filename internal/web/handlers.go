package web

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const articlesPerPage = 12

// handleIndex renders the main page with paginated articles.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}

	articles, total, err := s.store.ListArticlesPaginated(r.Context(), page, articlesPerPage)
	if err != nil {
		s.logger.Error("list articles for index", "error", err)
		http.Error(w, "Erreur interne du serveur", http.StatusInternalServerError)
		return
	}

	views := make([]ArticleView, 0, len(articles))
	for _, a := range articles {
		views = append(views, articleToView(a))
	}

	totalPages := (total + articlesPerPage - 1) / articlesPerPage
	if totalPages < 1 {
		totalPages = 1
	}

	data := IndexData{
		Articles:   views,
		Page:       page,
		TotalPages: totalPages,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
		PrevPage:   page - 1,
		NextPage:   page + 1,
	}

	if s.sched != nil && !s.sched.IsActive(time.Now()) {
		data.Offline = true
		data.NextStartAt = s.sched.NextStart(time.Now()).Format("15:04")
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.logger.Error("render index template", "error", err)
	}
}

// handleArticleDetail renders a single article detail page with related articles.
func (s *Server) handleArticleDetail(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/article/")
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	// Reconstruct S3 key from slug.
	key := slug + "-article.md"

	article, err := s.store.GetArticle(r.Context(), key)
	if err != nil {
		s.logger.Error("get article for detail", "key", key, "error", err)
		http.NotFound(w, r)
		return
	}

	// Get 3 random related articles for internal linking.
	related, err := s.store.GetRandomArticles(r.Context(), key, 3)
	if err != nil {
		s.logger.Warn("get related articles", "error", err)
	}

	relatedViews := make([]ArticleView, 0, len(related))
	for _, a := range related {
		relatedViews = append(relatedViews, articleToView(a))
	}

	// Derive base URL from request.
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)

	data := ArticleDetailData{
		Article: articleToView(article),
		Related: relatedViews,
		BaseURL: baseURL,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "article-detail.html", data); err != nil {
		s.logger.Error("render article detail template", "error", err)
	}
}

// sitemapURL represents a single URL entry in a sitemap.
type sitemapURL struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

// sitemapNews holds Google News metadata for a sitemap entry.
type sitemapNews struct {
	Publication sitemapPub `xml:"news:publication"`
	PubDate     string     `xml:"news:publication_date"`
	Title       string     `xml:"news:title"`
}

type sitemapPub struct {
	Name     string `xml:"news:name"`
	Language string `xml:"news:language"`
}

type sitemapNewsURL struct {
	Loc  string      `xml:"loc"`
	News sitemapNews `xml:"news:news"`
}

type sitemapIndex struct {
	XMLName xml.Name         `xml:"urlset"`
	Xmlns   string           `xml:"xmlns,attr"`
	News    string           `xml:"xmlns:news,attr"`
	URLs    []sitemapNewsURL `xml:"url"`
}

// handleSitemap generates a sitemap.xml with Google News extensions.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	articles, err := s.store.ListArticles(r.Context(), 1000)
	if err != nil {
		s.logger.Error("list articles for sitemap", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)

	sitemap := sitemapIndex{
		Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9",
		News:  "http://www.google.com/schemas/sitemap-news/0.9",
	}

	for _, a := range articles {
		slug := articleSlug(a.Key)
		sitemap.URLs = append(sitemap.URLs, sitemapNewsURL{
			Loc: baseURL + "/article/" + slug,
			News: sitemapNews{
				Publication: sitemapPub{
					Name:     "Freedom Radio AI",
					Language: "fr",
				},
				PubDate: a.CreatedAt.Format(time.RFC3339),
				Title:   a.Title,
			},
		})
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(sitemap); err != nil {
		s.logger.Error("encode sitemap", "error", err)
	}
}

// handleAudio proxies audio files from S3 storage with Range request support.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/audio/")
	if key == "" {
		http.NotFound(w, r)
		return
	}

	obj, err := s.store.GetObjectWithStat(r.Context(), key)
	if err != nil {
		s.logger.Error("get audio object", "key", key, "error", err)
		http.NotFound(w, r)
		return
	}
	defer obj.Close()

	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "audio.mp3", obj.ModTime, obj)
}

// handleCover proxies cover images from S3 storage with Range request support.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/cover/")
	if key == "" {
		http.NotFound(w, r)
		return
	}

	rc, err := s.store.GetObject(r.Context(), key)
	if err != nil {
		s.logger.Error("get cover object", "key", key, "error", err)
		http.NotFound(w, r)
		return
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		s.logger.Error("read cover object", "key", key, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "cover.jpg", time.Time{}, bytes.NewReader(data))
}
