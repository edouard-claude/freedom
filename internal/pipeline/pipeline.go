package pipeline

import (
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"freedom/internal/article"
	"freedom/internal/chunk"
	"freedom/internal/classify"
	"freedom/internal/config"
	"freedom/internal/creole"
	"freedom/internal/icecast"
	"freedom/internal/mistral"
	"freedom/internal/mp3"
	"freedom/internal/output"
	"freedom/internal/pool"
	"freedom/internal/storage"
	"freedom/internal/transcribe"
	"freedom/internal/web"
)

// Run wires up all pipeline stages and blocks until shutdown.
// statusFunc, if non-nil, is called to report pipeline startup progress.
func Run(ctx context.Context, cfg config.Config, store *storage.Client, hub *web.SSEHub, server *web.Server, logger *slog.Logger, statusFunc func(string)) error {
	// Load creole lexicon if configured.
	var contextBias []string
	var fewShotPrompt string
	if cfg.LexiconFile != "" {
		lex, err := creole.Load(cfg.LexiconFile)
		if err != nil {
			logger.Warn("creole lexicon not loaded", "error", err)
		} else {
			contextBias = lex.ContextBiasWords(100)
			fewShotPrompt = lex.FewShotPrompt()
			logger.Info("creole lexicon loaded", "entries", lex.Len(), "bias_words", len(contextBias))
		}
	}

	if statusFunc != nil {
		statusFunc("Initialisation du pipeline...")
	}

	bufPool := pool.New(32 * 1024)

	// Stage channels.
	rawCh := make(chan []byte, 4)
	frameCh := make(chan mp3.Frame, 64)
	chunkCh := make(chan chunk.Chunk, 4)
	resultCh := make(chan transcribe.TranscriptionResult, cfg.Workers*2)

	// Transcript channel for live SSE streaming.
	transcriptCh := make(chan string, 32)

	// Article pipeline channels.
	segAccumCh := make(chan article.Segment, 32)
	windowCh := make(chan article.Window, 4)
	sseArticleCh := make(chan storage.Article, 16)

	// Stage 1 components: Icecast reader.
	reader := icecast.NewReader(cfg.StreamURL, logger, bufPool.Get, bufPool.Put)

	// Stage 2: MP3 frame parser.
	parser := mp3.NewParser(logger)

	// Stage 3: Chunk accumulator.
	accum := chunk.NewAccumulator(
		time.Duration(float64(time.Second)*cfg.ChunkDuration),
		time.Duration(float64(time.Second)*cfg.Overlap),
	)

	// Shared rate-limited transport for all Mistral API calls (1 req/s global limit).
	throttle := mistral.NewThrottledTransport(time.Second)
	mistralHTTP := &http.Client{Timeout: 120 * time.Second, Transport: throttle}
	transcribeHTTP := &http.Client{Timeout: 60 * time.Second, Transport: throttle}

	// Stage 4: Transcription worker pool.
	client := transcribe.NewClient(cfg.MistralAPIKey, cfg.TranscribeModel, cfg.Language, contextBias, logger)
	client.SetHTTPClient(transcribeHTTP)
	workerPool := transcribe.NewWorkerPool(client, cfg.Workers, logger)

	// Stage 5: Output handler.
	outputHandler := output.NewHandler(logger, segAccumCh, transcriptCh)

	// Stage 6: Segment accumulator (sliding window).
	textAccum := article.NewAccumulator(cfg.ArticleWindow)

	// Stage 7 components: Classifier, chat client, image client.
	classifyChat := mistral.NewChatClient(cfg.MistralAPIKey, cfg.ClassifyModel, 0, 512, logger)
	classifyChat.SetHTTPClient(mistralHTTP)
	classifier := classify.NewClassifier(classifyChat, logger)

	articleChat := mistral.NewChatClient(cfg.MistralAPIKey, cfg.ArticleModel, 0.3, 2048, logger)
	articleChat.SetHTTPClient(mistralHTTP)
	imager := mistral.NewImageClient(cfg.HFToken, logger)
	// trimChat reuses classifyChat (mistral-small, temp=0, maxTokens=512) by design:
	// the trim response is ~20 tokens, same model/config is appropriate.
	articleGen := article.NewGenerator(classifier, articleChat, classifyChat, imager, store, cfg.ArticleModel, fewShotPrompt, logger, sseArticleCh)

	if statusFunc != nil {
		statusFunc("Connexion au flux radio...")
	}

	g, gctx := errgroup.WithContext(ctx)

	// Stage 1: Icecast reader.
	g.Go(func() error {
		return reader.Run(gctx, rawCh)
	})

	// Stage 2: MP3 frame parser.
	g.Go(func() error {
		return parser.Run(gctx, rawCh, frameCh, bufPool.Put)
	})

	// Stage 3: Chunk accumulator.
	g.Go(func() error {
		return accum.Run(gctx, frameCh, chunkCh)
	})

	// Stage 4: Transcription worker pool.
	g.Go(func() error {
		return workerPool.Run(gctx, chunkCh, resultCh)
	})

	// Stage 5: Output handler (also forwards segments to article pipeline).
	g.Go(func() error {
		defer close(segAccumCh)
		defer close(transcriptCh)
		return outputHandler.Run(gctx, resultCh)
	})

	// Stage 6: Segment accumulator (sliding window).
	g.Go(func() error {
		return textAccum.Run(gctx, segAccumCh, windowCh)
	})

	// Stage 7: Article generator (classify + detect + write + image + store).
	g.Go(func() error {
		defer close(sseArticleCh)
		return articleGen.Run(gctx, windowCh)
	})

	// Stage 8a: Transcript SSE relay.
	g.Go(func() error {
		firstTranscript := true
		for {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case text, ok := <-transcriptCh:
				if !ok {
					return nil
				}
				if firstTranscript && statusFunc != nil {
					statusFunc("Pipeline opérationnel — transcription en direct")
					firstTranscript = false
				}
				hub.NotifyTranscript(`<span class="transcript-chunk">` + html.EscapeString(text) + ` </span>`)
			}
		}
	})

	// Stage 8b: Web SSE notifier (articles).
	g.Go(func() error {
		for {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case sa, ok := <-sseArticleCh:
				if !ok {
					return nil
				}
				html, err := server.RenderArticleCard(sa)
				if err != nil {
					logger.Error("rendering article card for SSE", "error", err)
					continue
				}
				hub.Notify(html)

				// Also log as JSON for debugging.
				if data, err := json.Marshal(sa); err == nil {
					logger.Info("article broadcast via SSE", "article", string(data))
				}
			}
		}
	})

	return g.Wait()
}
