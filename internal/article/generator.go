package article

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"freedom/internal/classify"
	"freedom/internal/mistral"
	"freedom/internal/storage"
)

const detectSystemPromptTemplate = `Tu es un analyseur de transcription radio de La Réunion (Radio Freedom).
Tu reçois une transcription brute d'un segment radio. Classe ce contenu dans l'une des catégories suivantes :

"news" — Information factuelle : événement local/national/international, annonce officielle, fait divers, météo importante, politique, société.
"listener_call" — Appel à auditeurs / entraide : un auditeur demande de l'aide (panne, recherche), un objet trouvé/perdu, un SOS, une annonce solidaire, un témoignage d'entraide. C'est l'essence de Radio Freedom.
"none" — Musique, jingles, publicités, conversations informelles sans contenu, bavardage sans sujet précis.

Réponds UNIQUEMENT en JSON :
{"type": "news", "topic": "résumé court"} pour une actualité
{"type": "listener_call", "topic": "résumé court"} pour un appel/entraide
{"type": "none", "topic": ""} sinon
%s
Si le sujet principal a déjà été couvert ci-dessus, réponds type="none".`

const articleSystemPromptTemplate = `Tu es un journaliste de Freedom.fr, site d'information de La Réunion.
Tu rédiges des articles de presse à partir de transcriptions de Radio Freedom (basée à La Réunion).

CONTEXTE GÉOGRAPHIQUE (CRITIQUE — ne jamais confondre) :
- La Réunion : département français d'outre-mer dans l'océan Indien (Saint-Denis, Saint-Pierre, Le Tampon, Saint-Paul, Le Port, Les Avirons, etc.)
- Madagascar : pays indépendant voisin. Villes : Antananarivo (Tana), Tamatave (Toamasina/Toa Mazin), Antsirabe, etc. Tamatave N'EST PAS à La Réunion.
- Mayotte, Comores, Maurice : autres territoires/pays de la zone océan Indien
- La radio couvre l'actualité locale (La Réunion) ET régionale (Madagascar, océan Indien) ET nationale/internationale (France, monde)
- Localise TOUJOURS correctement chaque événement dans le bon pays/territoire. Ne place JAMAIS un événement malgache à La Réunion ou inversement.

FORMAT ARTICLE :
- Titre : long, descriptif, factuel (style Freedom.fr)
- Catégorie : "Actualités"
- Corps : 150-300 mots, style journalistique factuel, paragraphes courts
- Langue : français standard (même si la transcription contient du créole réunionnais)
- Summary : 1-2 phrases résumant l'article
- Cover prompt : comma-separated English tags describing ONLY the scene content (subjects, objects, setting, action, location). Do NOT include any style, palette, medium, or artistic direction. No text overlays, no identifiable faces. Example: "bustling outdoor tropical market, vendors selling exotic fruits and spices, parasols, crowd of people"

IMPORTANT :
- Extrais les faits de la transcription radio, ne les invente pas
- Si la transcription est en créole, traduis en français standard
- Style neutre et informatif, comme un article de presse locale
- Ne mentionne pas que l'information vient de la radio
- Vérifie la cohérence géographique : Tamatave = Madagascar, Le Tampon = La Réunion, etc.
- Traite UN SEUL sujet principal, le plus développé dans la transcription. Ignore les sujets secondaires.
- Ne reproduis JAMAIS de numéros de téléphone personnels, d'adresses e-mail ou de coordonnées privées.
- Texte brut uniquement : aucun formatage markdown (pas de **gras**, pas de *italique*, pas de listes à puces).

Réponds UNIQUEMENT en JSON :
{"title": "...", "category": "Actualités", "body": "...", "summary": "...", "cover_prompt": "..."}`

const listenerCallSystemPromptTemplate = `Tu es un rédacteur pour Freedom.fr, site communautaire de La Réunion.
Tu rédiges des fiches d'entraide à partir de transcriptions d'appels à auditeurs sur Radio Freedom.

Radio Freedom est célèbre pour ses appels à auditeurs : un auditeur appelle la radio pour demander de l'aide (panne de voiture, recherche d'objet perdu, SOS, entraide) et la communauté se mobilise. C'est l'essence même de cette radio.

FORMAT FICHE ENTRAIDE :
- Titre : court et percutant, décrivant le besoin ou la situation (ex: "Portefeuille retrouvé au BK du Port", "SOS panne de voiture à Saint-Pierre")
- Catégorie : "Entraide"
- Corps : 50-150 mots maximum. Résume clairement : qui a besoin d'aide, quel est le problème, où (commune/quartier si mentionné), quel type d'aide est recherché.
- Summary : 1 phrase résumant l'appel.
- Cover prompt : comma-separated English tags describing ONLY the scene content (subjects, objects, setting, action, location). Do NOT include any style, palette, medium, or artistic direction. No text overlays, no identifiable faces. Example: "hands reaching out to help, broken-down car, tropical road, community solidarity"

IMPORTANT :
- Extrais les faits de la transcription, ne les invente pas
- Si la transcription est en créole, traduis en français standard
- Ton chaleureux et solidaire, style communautaire
- Ne mentionne pas que l'information vient de la radio
- Ne reproduis JAMAIS de numéros de téléphone personnels, d'adresses e-mail ou de coordonnées privées. Anonymise les noms de famille.
- Texte brut uniquement : aucun formatage markdown.
- Traite UN SEUL appel/demande, le principal.

Réponds UNIQUEMENT en JSON :
{"title": "...", "category": "Entraide", "body": "...", "summary": "...", "cover_prompt": "..."}`

const coverStylePrefix = "Blue ink drawing on white paper, monochrome blue and white palette, indigo ink tones, fine pen illustration style, hand-drawn sketch aesthetic, "
const coverStyleSuffix = ", detailed linework, no color except shades of blue, editorial press illustration style"

const trimSystemPrompt = `Tu reçois des segments de transcription radio numérotés et le résumé d'un article. Identifie la plage contiguë de segments (start_index, end_index inclus) qui couvre le sujet de l'article. Inclus les segments de contexte immédiat mais exclus musique, pub, et sujets différents. Si le sujet est interrompu par un jingle ou une pub puis reprend, élargis la plage pour inclure l'interruption plutôt que de couper une partie du sujet.`

// detectResponseFormat is the json_schema for content detection.
var detectResponseFormat = &mistral.ResponseFormat{
	Type: "json_schema",
	JSONSchema: &mistral.JSONSchema{
		Name:   "content_detection",
		Strict: true,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"type": {"type": "string", "enum": ["news", "listener_call", "none"]},
				"topic": {"type": "string"}
			},
			"required": ["type", "topic"],
			"additionalProperties": false
		}`),
	},
}

// articleResponseFormat is the json_schema for article generation.
var articleResponseFormat = &mistral.ResponseFormat{
	Type: "json_schema",
	JSONSchema: &mistral.JSONSchema{
		Name:   "article",
		Strict: true,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"title": {"type": "string"},
				"category": {"type": "string"},
				"body": {"type": "string"},
				"summary": {"type": "string"},
				"cover_prompt": {"type": "string"}
			},
			"required": ["title", "category", "body", "summary", "cover_prompt"],
			"additionalProperties": false
		}`),
	},
}

// trimResponseFormat is the json_schema for audio trim range.
var trimResponseFormat = &mistral.ResponseFormat{
	Type: "json_schema",
	JSONSchema: &mistral.JSONSchema{
		Name:   "audio_trim",
		Strict: true,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"start_index": {"type": "integer"},
				"end_index": {"type": "integer"}
			},
			"required": ["start_index", "end_index"],
			"additionalProperties": false
		}`),
	},
}

type trimResponse struct {
	StartIndex int `json:"start_index"`
	EndIndex   int `json:"end_index"`
}

type detectResponse struct {
	Type  string `json:"type"`
	Topic string `json:"topic"`
}

type articleResponse struct {
	Title       string `json:"title"`
	Category    string `json:"category"`
	Body        string `json:"body"`
	Summary     string `json:"summary"`
	CoverPrompt string `json:"cover_prompt"`
}

// Generator detects news segments and generates articles via LLM.
type Generator struct {
	classifier   *classify.Classifier
	chat         *mistral.ChatClient
	trimChat     *mistral.ChatClient
	imager       *mistral.ImageClient
	store        *storage.Client
	logger       *slog.Logger
	modelName    string
	fewShot      string // creole lexicon injected into article prompt
	recentTopics []string
	articleCh    chan<- storage.Article
}

// NewGenerator creates an article generator.
func NewGenerator(
	classifier *classify.Classifier,
	chat *mistral.ChatClient,
	trimChat *mistral.ChatClient,
	imager *mistral.ImageClient,
	store *storage.Client,
	modelName string,
	fewShotPrompt string,
	logger *slog.Logger,
	articleCh chan<- storage.Article,
) *Generator {
	return &Generator{
		classifier: classifier,
		chat:       chat,
		trimChat:   trimChat,
		imager:     imager,
		store:      store,
		modelName:  modelName,
		fewShot:    fewShotPrompt,
		logger:     logger,
		articleCh:  articleCh,
	}
}

// Run reads transcription windows, classifies, detects news, generates articles,
// creates cover images, and stores everything in S3.
func (g *Generator) Run(ctx context.Context, windowCh <-chan Window) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case window, ok := <-windowCh:
			if !ok {
				return nil
			}

			art, err := g.processWindow(ctx, window)
			if err != nil {
				g.logger.Error("article generation failed", "error", err)
				continue
			}
			if art == nil {
				continue // not news or filtered out
			}

			// Notify SSE channel (non-blocking).
			if g.articleCh != nil {
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
				select {
				case g.articleCh <- sa:
				default:
				}
			}
		}
	}
}

func (g *Generator) processWindow(ctx context.Context, window Window) (*Article, error) {
	// Step 1: Classify content (speech/music/ads).
	g.logger.Debug("classifying window", "text_len", len(window.Text))

	result, err := g.classifier.Classify(ctx, window.Text)
	if err != nil {
		return nil, fmt.Errorf("classification: %w", err)
	}

	if result.Category != "speech" {
		g.logger.Debug("not speech, skipping", "category", result.Category, "confidence", result.Confidence)
		return nil, nil
	}
	if result.Confidence != "high" && result.Confidence != "medium" {
		g.logger.Debug("low confidence speech, skipping", "confidence", result.Confidence)
		return nil, nil
	}

	// Step 2: Detect if newsworthy.
	g.logger.Debug("detecting news segment", "text_len", len(window.Text))

	detectPrompt := g.buildDetectPrompt()
	detectResp, err := g.chat.Complete(ctx, detectPrompt, window.Text, detectResponseFormat)
	if err != nil {
		return nil, fmt.Errorf("detection call: %w", err)
	}

	var detect detectResponse
	if err := json.Unmarshal([]byte(detectResp), &detect); err != nil {
		return nil, fmt.Errorf("parsing detection response: %w", err)
	}

	if detect.Type == "none" {
		g.logger.Debug("not actionable content, skipping", "topic", detect.Topic)
		return nil, nil
	}

	g.logger.Info("content detected", "type", detect.Type, "topic", detect.Topic)

	// Step 3: Generate article or listener call.
	var systemPrompt string
	switch detect.Type {
	case "listener_call":
		systemPrompt = listenerCallSystemPromptTemplate
	default: // "news"
		systemPrompt = articleSystemPromptTemplate
	}
	if g.fewShot != "" {
		systemPrompt += "\n\n" + g.fewShot
	}
	userPrompt := fmt.Sprintf("Transcription radio à transformer en contenu :\n\n%s", window.Text)

	articleResp, err := g.chat.Complete(ctx, systemPrompt, userPrompt, articleResponseFormat)
	if err != nil {
		return nil, fmt.Errorf("article generation call: %w", err)
	}

	var artResp articleResponse
	if err := json.Unmarshal([]byte(articleResp), &artResp); err != nil {
		return nil, fmt.Errorf("parsing article response: %w", err)
	}

	now := time.Now()
	// S3 key layout: yy/mm/dd/hh-mm-ss-{type}.{ext}
	dir := now.Format("06/01/02")
	ts := now.Format("15-04-05")
	articleKey := dir + "/" + ts + "-article.md"
	audioKey := dir + "/" + ts + "-audio.mp3"
	coverKey := dir + "/" + ts + "-cover.jpg"

	// Trim audio to relevant segments.
	trimmedMP3 := g.trimAudio(ctx, window, artResp)

	art := &Article{
		Key:         articleKey,
		Title:       artResp.Title,
		Category:    artResp.Category,
		Body:        artResp.Body,
		Summary:     artResp.Summary,
		CoverPrompt: artResp.CoverPrompt,
		Topics:      []string{detect.Topic},
		ModelUsed:   g.modelName,
		CreatedAt:   now,
		UpdatedAt:   now,
		Status:      "published",
		RawMP3:      trimmedMP3,
	}

	// Step 4: Generate cover image.
	if artResp.CoverPrompt != "" {
		scene := strings.TrimRight(artResp.CoverPrompt, " .,;")
		coverPrompt := coverStylePrefix + scene + coverStyleSuffix
		g.logger.Info("generating cover image", "prompt_len", len(coverPrompt))
		jpegData, err := g.imager.Generate(ctx, coverPrompt)
		if err != nil {
			g.logger.Error("cover image generation failed", "error", err)
		} else {
			if err := g.store.PutCover(ctx, coverKey, jpegData); err != nil {
				g.logger.Error("storing cover image failed", "error", err)
			} else {
				art.CoverKey = coverKey
			}
		}
	}

	// Step 5: Store audio in S3.
	if len(art.RawMP3) > 0 {
		if err := g.store.PutAudio(ctx, audioKey, art.RawMP3); err != nil {
			g.logger.Error("storing audio failed", "error", err)
		} else {
			art.AudioKey = audioKey
		}
	}

	// Step 5b: Store article in S3.
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
	if err := g.store.PutArticle(ctx, sa); err != nil {
		return nil, fmt.Errorf("storing article: %w", err)
	}

	// Track topic for deduplication.
	g.recentTopics = append(g.recentTopics, detect.Topic)
	if len(g.recentTopics) > 5 {
		g.recentTopics = g.recentTopics[1:]
	}

	g.logger.Info("article published", "key", art.Key, "title", art.Title)
	return art, nil
}

// trimAudio asks the LLM which segments are relevant, then concatenates only those.
// Falls back to full audio on any error.
func (g *Generator) trimAudio(ctx context.Context, window Window, artResp articleResponse) []byte {
	if len(window.Segments) == 0 {
		g.logger.Debug("trim skipped: no individual segments in window")
		return window.RawMP3
	}

	if g.trimChat == nil {
		g.logger.Warn("trim skipped: trimChat not configured")
		return window.RawMP3
	}

	// Build numbered segment list.
	var sb strings.Builder
	for i, seg := range window.Segments {
		fmt.Fprintf(&sb, "[%d] %s\n", i, seg.Text)
	}

	userPrompt := fmt.Sprintf("Segments de transcription :\n%s\nArticle — Titre : %s\nRésumé : %s",
		sb.String(), artResp.Title, artResp.Summary)

	resp, err := g.trimChat.Complete(ctx, trimSystemPrompt, userPrompt, trimResponseFormat)
	if err != nil {
		g.logger.Warn("trim LLM call failed, keeping full audio", "error", err)
		return window.RawMP3
	}

	var tr trimResponse
	if err := json.Unmarshal([]byte(resp), &tr); err != nil {
		g.logger.Warn("trim response parse failed, keeping full audio", "error", err)
		return window.RawMP3
	}

	if tr.StartIndex < 0 || tr.EndIndex < tr.StartIndex || tr.EndIndex >= len(window.Segments) {
		g.logger.Warn("trim returned invalid range, keeping full audio",
			"start", tr.StartIndex, "end", tr.EndIndex, "segments", len(window.Segments))
		return window.RawMP3
	}

	g.logger.Info("trimming audio", "start", tr.StartIndex, "end", tr.EndIndex, "total", len(window.Segments))

	totalSize := 0
	for i := tr.StartIndex; i <= tr.EndIndex; i++ {
		totalSize += len(window.Segments[i].RawMP3)
	}
	trimmed := make([]byte, 0, totalSize)
	for i := tr.StartIndex; i <= tr.EndIndex; i++ {
		trimmed = append(trimmed, window.Segments[i].RawMP3...)
	}
	return trimmed
}

func (g *Generator) buildDetectPrompt() string {
	topicsSection := ""
	if len(g.recentTopics) > 0 {
		topicsSection = "\nSujets déjà couverts récemment (NE PAS générer d'article si le sujet est le même ou très similaire) :"
		for _, t := range g.recentTopics {
			topicsSection += "\n- " + t
		}
		topicsSection += "\n"
	}
	return fmt.Sprintf(detectSystemPromptTemplate, topicsSection)
}

// slugify converts a title to a URL-friendly slug.
func slugify(s string) string {
	s = stripAccents(s)
	s = strings.ToLower(s)

	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	result := strings.TrimRight(b.String(), "-")
	if len(result) > 50 {
		if i := strings.LastIndex(result[:51], "-"); i > 0 {
			result = result[:i]
		} else {
			result = result[:50]
		}
	}
	return result
}
