# Freedom — Pipeline IA de Journalisme Radio en Temps Réel

Pipeline Go concurrent à 8 étages qui écoute le flux MP3 live de [Radio Freedom](https://www.freedom.fr/) (La Réunion), transcrit l'audio, classifie le contenu, génère des articles d'actualité et des fiches d'entraide communautaire avec images de couverture, et diffuse le tout via une interface web temps réel — propulsé par Mistral AI (transcription, classification, rédaction) et FLUX.1-schnell (génération d'images).

> **Avertissement** : Démonstration technique. Les articles générés ne constituent pas un contenu éditorial et peuvent contenir des inexactitudes. L'audio source est fourni avec chaque article par souci de transparence.

## Architecture

Pipeline de 8 goroutines communicant par channels typés, orchestré via `errgroup` :

```
Flux MP3 Radio Freedom (Icecast)
        │
  ┌─────▼──────┐
  │  Icecast   │  Étage 1 — Lecture HTTP chunked, stripping métadonnées ICY
  │  Reader    │            Reconnexion backoff exponentiel + jitter
  └─────┬──────┘
        │ raw bytes (buffers 32 Ko via sync.Pool)
  ┌─────▼──────┐
  │  MP3 Frame │  Étage 2 — Ring buffer 128 Ko, détection sync word (0xFF 0xE0)
  │  Parser    │            Validation deux trames consécutives
  └─────┬──────┘
        │ frames (header parsé : bitrate, sample rate, durée)
  ┌─────▼──────┐
  │  Chunk     │  Étage 3 — Accumulation jusqu'à durée cible (10s)
  │  Accum.    │            Recouvrement configurable (1s) pour continuité
  └─────┬──────┘
        │ chunks (~10s audio + MP3 brut, numéro de séquence)
  ┌─────▼──────┐
  │  Voxtral   │  Étage 4 — Pool de N workers parallèles, upload multipart
  │  Transcr.  │            Retry backoff exponentiel, gestion rate limit (429)
  └─────┬──────┘
        │ résultats de transcription (texte + MP3 + latence)
  ┌─────▼──────┐
  │  Output    │  Étage 5 — Réordonnancement par numéro de séquence
  │  Handler   │            Transfert texte + audio vers pipeline article
  └─────┬──────┘
        │ segments (texte + MP3 brut)
  ┌─────▼──────┐
  │  Sliding   │  Étage 6 — Accumulation de N segments en fenêtres
  │  Window    │            Recouvrement 50% entre fenêtres
  └─────┬──────┘
        │ fenêtres (texte concaténé + audio)
  ┌─────▼──────┐
  │  Article   │  Étage 7 — Classifier → Détecter → Générer article
  │  Generator │            → Générer image de couverture → Stocker S3
  └─────┬──────┘
        │ articles
  ┌─────▼──────┐
  │  Web UI    │  Étage 8 — HTMX + SSE push temps réel
  │  + SSE Hub │            Lecteur radio live, cartes d'articles
  └────────────┘
```

### Dimensionnement des channels

| Channel | Buffer | Rôle |
|---------|--------|------|
| `rawCh` | 4 | Bytes bruts Icecast → Parser |
| `frameCh` | 64 | Trames MP3 → Accumulateur |
| `chunkCh` | 4 | Chunks audio → Pool de transcription |
| `resultCh` | Workers×2 | Résultats transcription → Réordonnanceur |
| `transcriptCh` | 32 | Transcriptions live → SSE |
| `segAccumCh` | 32 | Segments → Fenêtre glissante |
| `windowCh` | 4 | Fenêtres → Générateur d'articles |
| `sseArticleCh` | 16 | Articles finaux → Push web |

## Points Techniques Clés

### Parser MP3 — Ring Buffer & Validation Deux Trames

Parser MP3 au niveau trame, implémenté from scratch sans dépendance externe :

- **Ring buffer 128 Ko** avec compaction au seuil de 64 Ko — élimine les allocations pendant le streaming
- **Détection sync word** : scan linéaire sur les 11 bits de sync (`0xFF` + `0xE0` mask), avec skip byte-par-byte des données invalides
- **Validation deux trames** : après parsing d'un header, vérifie qu'un header valide existe à l'offset `position + taille_trame`. Élimine les faux positifs sur des patterns aléatoires
- **Tables de lookup** pour bitrate et sample rate par version MPEG / couche
- **Calcul de taille** : `(samples/8 × bitrate×1000 / sampleRate) + padding`
- **Recyclage zero-copy** : buffers 32 Ko via `sync.Pool`, libérés immédiatement après parsing

### Lecteur Icecast — Stripping ICY & Reconnexion Résiliente

- **Machine à états ICY** : alterne entre `stateAudio` (lecture de N octets audio) et `stateMeta` (lecture d'1 octet de longueur × 16, skip des métadonnées). Si `metaInt=0`, pass-through intégral
- **Backoff exponentiel avec jitter** : min 1s, max 30s, facteur ×2, jitter ±25% aléatoire pour éviter le thundering herd
- **Boucle de reconnexion infinie** : seule l'annulation du context interrompt les tentatives

### Pool de Transcription — Workers Parallèles

- **N goroutines** (défaut 3) consommant un channel partagé — distribution uniforme, pas de load balancing nécessaire
- **Upload multipart** vers `voxtral-mini-2602` : fichier MP3, langue, context bias (lexique créole optionnel)
- **Retry** : 6 tentatives, backoff `5 × 2^n` secondes (5s → 160s), respect du header `Retry-After` sur 429
- **Mesure de latence** par requête pour monitoring de performance

### Sorties Structurées — json_schema Strict

Toutes les interactions LLM utilisent le mode `json_schema` de Mistral AI avec `strict: true` — garantie de conformité du format de sortie :

```json
{
  "type": "json_schema",
  "json_schema": {
    "name": "article",
    "strict": true,
    "schema": {
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
    }
  }
}
```

Pas de parsing JSON fragile ni de regex sur du texte libre — le modèle est contraint par le schéma au niveau de la génération.

### Hub SSE — Diffusion Temps Réel Sans Blocage

- **Event loop mono-thread** : un select sur 5 channels (register, unregister, broadcast, countReq, ctx.Done) — pas de mutex, pas de race condition
- **Drop des consommateurs lents** : si le channel client (buffer 16) est plein, le client est déconnecté et fermé. Le pipeline ne bloque jamais sur un navigateur lent
- **Keepalive** : ticker 15s envoie un commentaire SSE vide pour maintenir la connexion
- **Désactivation du WriteDeadline** : `SetWriteDeadline(time.Time{})` pour les connexions longue durée

### Génération d'Images — FLUX.1-schnell

- **Modèle** : [FLUX.1-schnell](https://huggingface.co/black-forest-labs/FLUX.1-schnell) (open source, Apache 2.0) via Hugging Face Inference API
- **Conversion automatique** : PNG → JPEG qualité 85% si nécessaire
- **Fallback gracieux** : après 3 tentatives, génération d'une image placeholder 800×400 grise. L'absence de `HF_TOKEN` retourne le placeholder immédiatement — jamais bloquant
- **Prompts éditoriaux** : style "dessin à l'encre bleue sur papier blanc", sans texte superposé, sans visages identifiables

## Modèles IA

| Rôle | Modèle | Fournisseur | Température | Max tokens |
|------|--------|-------------|-------------|------------|
| Transcription | `voxtral-mini-2602` | Mistral AI | — | — |
| Classification | `mistral-small-latest` | Mistral AI | 0 (déterministe) | 512 |
| Détection de contenu | `mistral-medium-latest` | Mistral AI | 0.3 | 2048 |
| Rédaction d'articles | `mistral-medium-latest` | Mistral AI | 0.3 | 2048 |
| Génération d'images | `FLUX.1-schnell` | Black Forest Labs | — | — |
| Validation de sortie | Tous les appels LLM | Mistral AI | — | `json_schema` strict |

## Pipeline de Génération d'Articles

Chaque fenêtre audio (12 segments × 10s = ~2 min) traverse 5 étapes :

1. **Classifier** — `mistral-small` détermine si le contenu est parole, musique, publicité ou silence. Seule la parole avec confiance haute/moyenne passe.
2. **Détecter** — `mistral-medium` classifie le transcript en `news`, `listener_call` (entraide) ou `none`. Un buffer FIFO des 5 derniers sujets empêche les doublons.
3. **Générer** — `mistral-medium` produit du contenu structuré via `json_schema` : titre, corps, résumé, prompt d'image. Les actualités donnent un article complet ; les appels d'auditeurs une fiche communautaire courte.
4. **Illustrer** — FLUX.1-schnell génère une image JPEG de couverture. Fallback sur placeholder si indisponible.
5. **Stocker** — Markdown + audio + image uploadés vers S3. Le hub SSE notifie les navigateurs connectés.

Les numéros de téléphone sont automatiquement expurgés du texte.

## Shutdown Gracieux & Propagation de Contexte

```
signal.NotifyContext(SIGINT, SIGTERM)
        │
        ▼
  errgroup.WithContext(ctx)
        │
        ├── Étage 1 : ctx.Done() → ferme rawCh
        ├── Étage 2 : rawCh fermé → drain → ferme frameCh
        ├── ...
        └── Étage 8 : ferme tous les clients SSE
```

- **Annulation en cascade** : chaque étage vérifie `<-ctx.Done()` dans sa boucle
- **Flush du réordonnanceur** : timeout de 30s pour émettre les résultats en attente
- **Shutdown web** : deadline de 10s pour les connexions actives
- Pas de fermeture explicite de channels nécessaire — le context pilote toute la terminaison

## Performances Typiques

| Métrique | Valeur |
|----------|--------|
| Latence bout-en-bout (parole → article publié) | ~3-4 min |
| Taille des chunks audio | ~10s × bitrate (80-400 Ko) |
| Ring buffer MP3 | 128 Ko fixe |
| Fenêtre d'accumulation | 12 segments × 10s = ~2 min |
| Workers de transcription | 3 (configurable) |
| Retry transcription | 6 tentatives, backoff 5s → 160s |
| Retry chat LLM | 3 tentatives, backoff 1s → 4s |
| Retry image | 3 tentatives + fallback placeholder |
| Reconnexion Icecast | Infinie, backoff 1s → 30s + jitter ±25% |

## Interface Web

- **Lecteur radio live** — écoute de Radio Freedom directement dans le navigateur
- **Articles temps réel** — les articles apparaissent instantanément via SSE (pas de rechargement)
- **Entraide** — les appels d'auditeurs sont affichés avec un style distinct vert
- **Audio source** — chaque article inclut son extrait MP3 pour vérification
- **Images de couverture** — illustrations éditoriales générées par FLUX.1-schnell
- **Design responsive** — mobile et desktop
- **Templates embarqués** — `//go:embed` pour HTML et CSS, aucun asset externe

## Stockage

Articles stockés dans S3/MinIO en Markdown avec frontmatter YAML :

```
s3://freedom/
  └── yy/mm/dd/
        ├── hh-mm-ss-article.md    # Markdown + frontmatter YAML
        ├── hh-mm-ss-sample.mp3    # Segment audio source
        └── hh-mm-ss-cover.jpg     # Image de couverture générée
```

Pas de base de données — la clé S3 encode la chronologie.

## Prérequis

- Go 1.25+
- Une instance MinIO (ou compatible S3)
- Une clé API Mistral AI
- Un token Hugging Face (gratuit, pour la génération d'images)

## Démarrage Rapide

```bash
# Build
cd freedom
go build -o freedom ./cmd/freedom

# Configurer (copier et remplir .env.sample)
cp .env.sample .env
# Renseigner les credentials dans .env

# Lancer
source .env && ./freedom
```

Ouvrir http://localhost:8080 pour le dashboard live.

## Configuration

Tous les paramètres sont configurables via flags ou variables d'environnement.

### Variables d'Environnement

| Variable | Description |
|----------|-------------|
| `MISTRAL_API_KEY` | **Requis.** Clé API Mistral AI |
| `HF_TOKEN` | Token Hugging Face pour la génération d'images (optionnel — fallback sur placeholder) |
| `S3_ENDPOINT` | **Requis.** Endpoint MinIO/S3 (ex. `localhost:9000`) |
| `S3_ACCESS_KEY` | Clé d'accès S3 |
| `S3_SECRET_KEY` | Clé secrète S3 |

### Flags

| Flag | Défaut | Description |
|------|--------|-------------|
| `-stream-url` | `https://freedomice.streamakaci.com/freedom.mp3` | URL du flux Icecast |
| `-chunk-duration` | `10` | Durée des chunks audio (secondes) |
| `-overlap` | `1.0` | Recouvrement entre chunks (secondes) |
| `-workers` | `3` | Workers de transcription parallèles |
| `-language` | `fr` | Langue de transcription |
| `-transcribe-model` | `voxtral-mini-2602` | Modèle Voxtral |
| `-classify-model` | `mistral-small-latest` | Modèle de classification |
| `-article-model` | `mistral-medium-latest` | Modèle de génération d'articles |
| | | *(Génération d'images via FLUX.1-schnell + HF_TOKEN)* |
| `-article-window` | `12` | Segments par fenêtre d'article |
| `-s3-bucket` | `freedom` | Nom du bucket S3 |
| `-s3-use-ssl` | `false` | TLS pour S3 |
| `-http-port` | `8080` | Port du serveur web |
| `-log-level` | `info` | Niveau de log (debug, info, warn, error) |

## Structure du Projet

```
freedom/
├── cmd/freedom/main.go           # Point d'entrée, gestion signaux, câblage
├── internal/
│   ├── config/                   # Flags + env
│   ├── icecast/                  # Lecteur Icecast + stripping ICY
│   ├── mp3/                      # Parser MP3 niveau trame (ring buffer, sync)
│   ├── chunk/                    # Accumulateur de trames avec recouvrement
│   ├── pool/                     # Recycleur de buffers sync.Pool
│   ├── transcribe/               # Client Voxtral + pool de workers
│   ├── classify/                 # Classifieur de contenu (parole/musique/pub)
│   ├── mistral/                  # Clients chat completions + génération d'images
│   ├── article/                  # Accumulateur, générateur, writer
│   ├── storage/                  # Client S3/MinIO, format Markdown
│   ├── output/                   # Réordonnanceur par numéro de séquence
│   ├── pipeline/                 # Orchestration 8 étages errgroup
│   └── web/                      # Serveur HTMX + SSE
│       ├── templates/            # Go html/template (index + carte article)
│       └── static/               # CSS
├── go.mod
└── go.sum
```

## Tests

```bash
go test ./... -count=1
```

65+ tests couvrant le parsing MP3, l'accumulation de chunks, les clients API Mistral, la génération d'images FLUX (mocks httptest), la classification de contenu, la sérialisation S3, le hub SSE et les handlers web.

## Dépendances

3 dépendances directes uniquement — tout le reste est bibliothèque standard Go :

- [`github.com/minio/minio-go/v7`](https://github.com/minio/minio-go) — Client S3/MinIO
- [`golang.org/x/sync`](https://pkg.go.dev/golang.org/x/sync) — errgroup pour l'orchestration du pipeline
- [`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text) — Normalisation Unicode (génération de slugs)

Pas de framework HTTP, pas de parser JSON externe, pas de base de données — `net/http`, `encoding/json`, `html/template` et `log/slog` de la stdlib suffisent.

## Licence

Démonstration technique. Le contenu de Radio Freedom appartient à ses propriétaires respectifs.
