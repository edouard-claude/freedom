package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config holds MinIO/S3 connection settings.
type S3Config struct {
	Endpoint  string // e.g. "minio.example.com:9000"
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// Article represents a stored article with frontmatter metadata.
type Article struct {
	Key       string    // S3 key path
	Title     string
	Category  string
	Summary   string
	Body      string
	ModelUsed string
	CreatedAt time.Time
	UpdatedAt time.Time
	AudioKey  string
	CoverKey  string
	Topics    []string
	Status    string
}

// Client wraps MinIO S3 operations.
type Client struct {
	minio  *minio.Client
	bucket string
	logger *slog.Logger
}

// NewClient connects to MinIO and ensures the bucket exists.
func NewClient(cfg S3Config, logger *slog.Logger) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to MinIO: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	exists, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("checking bucket: %w", err)
	}
	if !exists {
		if err := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("creating bucket %q: %w", cfg.Bucket, err)
		}
		logger.Info("created bucket", "bucket", cfg.Bucket)
	}

	return &Client{
		minio:  mc,
		bucket: cfg.Bucket,
		logger: logger,
	}, nil
}

// PutArticle stores an article as markdown at /yy/mm/dd/hh-mm-ss-article.md.
func (c *Client) PutArticle(ctx context.Context, article Article) error {
	md := marshalArticle(article)
	reader := bytes.NewReader([]byte(md))

	_, err := c.minio.PutObject(ctx, c.bucket, article.Key, reader, int64(len(md)), minio.PutObjectOptions{
		ContentType: "text/markdown",
	})
	if err != nil {
		return fmt.Errorf("putting article %q: %w", article.Key, err)
	}
	c.logger.Info("stored article", "key", article.Key)
	return nil
}

// PutAudio stores audio data at the given key.
func (c *Client) PutAudio(ctx context.Context, key string, data []byte) error {
	reader := bytes.NewReader(data)
	_, err := c.minio.PutObject(ctx, c.bucket, key, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: "audio/mpeg",
	})
	if err != nil {
		return fmt.Errorf("putting audio %q: %w", key, err)
	}
	c.logger.Info("stored audio", "key", key)
	return nil
}

// PutCover stores cover image data at the given key.
func (c *Client) PutCover(ctx context.Context, key string, data []byte) error {
	reader := bytes.NewReader(data)
	_, err := c.minio.PutObject(ctx, c.bucket, key, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: "image/jpeg",
	})
	if err != nil {
		return fmt.Errorf("putting cover %q: %w", key, err)
	}
	c.logger.Info("stored cover", "key", key)
	return nil
}

// GetArticle retrieves and parses a markdown article from S3.
func (c *Client) GetArticle(ctx context.Context, key string) (Article, error) {
	obj, err := c.minio.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return Article{}, fmt.Errorf("getting article %q: %w", key, err)
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		return Article{}, fmt.Errorf("reading article %q: %w", key, err)
	}

	article, err := unmarshalArticle(string(data))
	if err != nil {
		return Article{}, fmt.Errorf("parsing article %q: %w", key, err)
	}
	article.Key = key
	return article, nil
}

// ListArticles lists the most recent articles up to limit, sorted newest first.
func (c *Client) ListArticles(ctx context.Context, limit int) ([]Article, error) {
	return c.listArticlesFiltered(ctx, limit, time.Time{})
}

// ListArticlesSince returns articles created after the given time, newest first.
func (c *Client) ListArticlesSince(ctx context.Context, since time.Time) ([]Article, error) {
	return c.listArticlesFiltered(ctx, 0, since)
}

func (c *Client) listArticlesFiltered(ctx context.Context, limit int, since time.Time) ([]Article, error) {
	var keys []string
	for obj := range c.minio.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("listing objects: %w", obj.Err)
		}
		if strings.HasSuffix(obj.Key, "-article.md") {
			keys = append(keys, obj.Key)
		}
	}

	// Sort keys descending (newest first, since keys are date-based).
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))

	var articles []Article
	for _, key := range keys {
		article, err := c.GetArticle(ctx, key)
		if err != nil {
			c.logger.Warn("skipping unreadable article", "key", key, "error", err)
			continue
		}

		if !since.IsZero() && !article.CreatedAt.After(since) {
			continue
		}

		articles = append(articles, article)
		if limit > 0 && len(articles) >= limit {
			break
		}
	}

	return articles, nil
}

// listAllArticleKeys returns all article keys sorted newest first.
func (c *Client) listAllArticleKeys(ctx context.Context) ([]string, error) {
	var keys []string
	for obj := range c.minio.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("listing objects: %w", obj.Err)
		}
		if strings.HasSuffix(obj.Key, "-article.md") {
			keys = append(keys, obj.Key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys, nil
}

// CountArticles returns the total number of articles in the bucket.
func (c *Client) CountArticles(ctx context.Context) (int, error) {
	keys, err := c.listAllArticleKeys(ctx)
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}

// ListArticlesPaginated returns a page of articles (1-indexed) sorted newest first.
func (c *Client) ListArticlesPaginated(ctx context.Context, page, perPage int) ([]Article, int, error) {
	keys, err := c.listAllArticleKeys(ctx)
	if err != nil {
		return nil, 0, err
	}

	total := len(keys)
	start := (page - 1) * perPage
	if start >= total {
		return nil, total, nil
	}
	end := start + perPage
	if end > total {
		end = total
	}

	var articles []Article
	for _, key := range keys[start:end] {
		article, err := c.GetArticle(ctx, key)
		if err != nil {
			c.logger.Warn("skipping unreadable article", "key", key, "error", err)
			continue
		}
		articles = append(articles, article)
	}

	return articles, total, nil
}

// GetRandomArticles returns up to count random articles, excluding the given key.
func (c *Client) GetRandomArticles(ctx context.Context, excludeKey string, count int) ([]Article, error) {
	keys, err := c.listAllArticleKeys(ctx)
	if err != nil {
		return nil, err
	}

	// Filter out the excluded key.
	var candidates []string
	for _, k := range keys {
		if k != excludeKey {
			candidates = append(candidates, k)
		}
	}

	// Shuffle and pick up to count.
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	if len(candidates) > count {
		candidates = candidates[:count]
	}

	var articles []Article
	for _, key := range candidates {
		article, err := c.GetArticle(ctx, key)
		if err != nil {
			c.logger.Warn("skipping unreadable article", "key", key, "error", err)
			continue
		}
		articles = append(articles, article)
	}
	return articles, nil
}

// UpdateArticle re-writes the markdown article with updated content and sets
// the updated_at field.
func (c *Client) UpdateArticle(ctx context.Context, key string, article Article) error {
	article.Key = key
	article.UpdatedAt = time.Now().UTC()
	return c.PutArticle(ctx, article)
}

// GetObject retrieves a raw object from S3 by key and returns a ReadCloser.
// The caller is responsible for closing the returned reader.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.minio.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting object %q: %w", key, err)
	}
	// Stat to verify the object exists (GetObject doesn't fail on missing keys).
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, fmt.Errorf("stat object %q: %w", key, err)
	}
	return obj, nil
}

// S3Object wraps a minio object with its stat info for use with http.ServeContent.
type S3Object struct {
	*minio.Object
	ModTime time.Time
}

// GetObjectWithStat retrieves an object from S3 and returns it as a seekable
// reader with modification time, suitable for http.ServeContent (supports Range).
func (c *Client) GetObjectWithStat(ctx context.Context, key string) (*S3Object, error) {
	obj, err := c.minio.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting object %q: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		return nil, fmt.Errorf("stat object %q: %w", key, err)
	}
	return &S3Object{Object: obj, ModTime: info.LastModified}, nil
}

// GetFileURL returns a presigned URL for the given key, valid for 24 hours.
func (c *Client) GetFileURL(key string) string {
	url, err := c.minio.PresignedGetObject(context.Background(), c.bucket, key, 24*time.Hour, nil)
	if err != nil {
		c.logger.Error("generating presigned URL", "key", key, "error", err)
		return ""
	}
	return url.String()
}

// marshalArticle serializes an Article into markdown with YAML frontmatter.
func marshalArticle(a Article) string {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("title: %q\n", a.Title))
	b.WriteString(fmt.Sprintf("category: %q\n", a.Category))
	b.WriteString(fmt.Sprintf("summary: %q\n", a.Summary))
	b.WriteString(fmt.Sprintf("model_used: %q\n", a.ModelUsed))
	b.WriteString(fmt.Sprintf("created_at: %q\n", a.CreatedAt.Format(time.RFC3339)))

	updatedAt := ""
	if !a.UpdatedAt.IsZero() {
		updatedAt = a.UpdatedAt.Format(time.RFC3339)
	}
	b.WriteString(fmt.Sprintf("updated_at: %q\n", updatedAt))

	b.WriteString(fmt.Sprintf("audio_key: %q\n", a.AudioKey))
	b.WriteString(fmt.Sprintf("cover_key: %q\n", a.CoverKey))

	b.WriteString("topics: [")
	for i, t := range a.Topics {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("%q", t))
	}
	b.WriteString("]\n")

	b.WriteString(fmt.Sprintf("status: %q\n", a.Status))
	b.WriteString("---\n")
	b.WriteString(a.Body)

	return b.String()
}

// unmarshalArticle parses markdown with YAML frontmatter into an Article.
func unmarshalArticle(md string) (Article, error) {
	// Split on frontmatter delimiters.
	const delim = "---\n"

	if !strings.HasPrefix(md, delim) {
		return Article{}, fmt.Errorf("missing frontmatter opening delimiter")
	}

	rest := md[len(delim):]
	idx := strings.Index(rest, delim)
	if idx < 0 {
		return Article{}, fmt.Errorf("missing frontmatter closing delimiter")
	}

	frontmatter := rest[:idx]
	body := rest[idx+len(delim):]

	a := Article{Body: body}
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}

		// Strip surrounding quotes.
		value = unquote(value)

		switch key {
		case "title":
			a.Title = value
		case "category":
			a.Category = value
		case "summary":
			a.Summary = value
		case "model_used":
			a.ModelUsed = value
		case "created_at":
			if t, err := time.Parse(time.RFC3339, value); err == nil {
				a.CreatedAt = t
			}
		case "updated_at":
			if value != "" {
				if t, err := time.Parse(time.RFC3339, value); err == nil {
					a.UpdatedAt = t
				}
			}
		case "audio_key":
			a.AudioKey = value
		case "cover_key":
			a.CoverKey = value
		case "topics":
			a.Topics = parseTopics(value)
		case "status":
			a.Status = value
		}
	}

	return a, nil
}

// unquote strips surrounding double quotes from a string.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
		// Unescape embedded quotes.
		s = strings.ReplaceAll(s, `\"`, `"`)
	}
	return s
}

// parseTopics extracts topic strings from a YAML-like array: ["a", "b"].
func parseTopics(s string) []string {
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if strings.TrimSpace(s) == "" {
		return nil
	}

	var topics []string
	for _, part := range strings.Split(s, ",") {
		t := strings.TrimSpace(part)
		t = unquote(t)
		if t != "" {
			topics = append(topics, t)
		}
	}
	return topics
}
