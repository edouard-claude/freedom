package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// sseMessage carries a typed SSE event (e.g. "article", "transcript").
type sseMessage struct {
	Event string
	Data  string
}

// SSEHub manages SSE connections for real-time article updates.
type SSEHub struct {
	clients    map[chan sseMessage]struct{}
	register   chan chan sseMessage
	unregister chan chan sseMessage
	broadcast  chan sseMessage
	countReq   chan chan int
	presence   chan bool
	logger     *slog.Logger
}

// NewSSEHub creates a new SSE hub ready to accept connections.
func NewSSEHub(logger *slog.Logger) *SSEHub {
	return &SSEHub{
		clients:    make(map[chan sseMessage]struct{}),
		register:   make(chan chan sseMessage),
		unregister: make(chan chan sseMessage),
		broadcast:  make(chan sseMessage, 128),
		countReq:   make(chan chan int),
		presence:   make(chan bool, 16),
		logger:     logger,
	}
}

// Run starts the hub event loop. It blocks until ctx is cancelled.
func (h *SSEHub) Run(ctx context.Context) {
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			// Close all client channels on shutdown.
			for ch := range h.clients {
				close(ch)
				delete(h.clients, ch)
			}
			return

		case ch := <-h.register:
			wasEmpty := len(h.clients) == 0
			h.clients[ch] = struct{}{}
			h.logger.Info("sse client connected", "total", len(h.clients))
			if wasEmpty {
				select {
				case h.presence <- true:
				default:
					h.logger.Warn("presence channel full, dropping wake signal")
				}
			}

		case ch := <-h.unregister:
			if _, ok := h.clients[ch]; ok {
				close(ch)
				delete(h.clients, ch)
				h.logger.Info("sse client disconnected", "total", len(h.clients))
				if len(h.clients) == 0 {
					select {
					case h.presence <- false:
					default:
						h.logger.Warn("presence channel full, dropping sleep signal")
					}
				}
			}

		case msg := <-h.broadcast:
			for ch := range h.clients {
				select {
				case ch <- msg:
				default:
					// Client too slow, drop it.
					close(ch)
					delete(h.clients, ch)
					h.logger.Warn("sse client dropped (slow consumer)", "total", len(h.clients))
					if len(h.clients) == 0 {
						select {
						case h.presence <- false:
						default:
							h.logger.Warn("presence channel full, dropping sleep signal")
						}
					}
				}
			}

		case resp := <-h.countReq:
			resp <- len(h.clients)

		case <-keepalive.C:
			ka := sseMessage{} // empty Event signals keepalive
			for ch := range h.clients {
				select {
				case ch <- ka:
				default:
					close(ch)
					delete(h.clients, ch)
					h.logger.Warn("sse client dropped (slow consumer, keepalive)", "total", len(h.clients))
					if len(h.clients) == 0 {
						select {
						case h.presence <- false:
						default:
							h.logger.Warn("presence channel full, dropping sleep signal")
						}
					}
				}
			}
		}
	}
}

// ClientCount returns the current number of connected SSE clients.
// Safe to call from any goroutine (synchronized via the hub's event loop).
// Returns 0 if the hub is not running (avoids deadlock).
func (h *SSEHub) ClientCount() int {
	resp := make(chan int, 1)
	select {
	case h.countReq <- resp:
		return <-resp
	case <-time.After(2 * time.Second):
		return 0
	}
}

// Presence returns a read-only channel that signals presence transitions.
// true = first client connected (0→1), false = last client disconnected (N→0).
func (h *SSEHub) Presence() <-chan bool {
	return h.presence
}

// NotifyStatus sends a status message to all connected SSE clients.
func (h *SSEHub) NotifyStatus(msg string) {
	select {
	case h.broadcast <- sseMessage{Event: "status", Data: msg}:
	default:
		h.logger.Warn("sse broadcast channel full, dropping status")
	}
}

// Notify sends a new article HTML fragment to all connected SSE clients.
func (h *SSEHub) Notify(articleHTML string) {
	select {
	case h.broadcast <- sseMessage{Event: "article", Data: articleHTML}:
	default:
		h.logger.Warn("sse broadcast channel full, dropping notification")
	}
}

// NotifyTranscript sends a transcript HTML fragment to all connected SSE clients.
func (h *SSEHub) NotifyTranscript(html string) {
	select {
	case h.broadcast <- sseMessage{Event: "transcript", Data: html}:
	default:
		h.logger.Warn("sse broadcast channel full, dropping transcript")
	}
}

// ServeHTTP handles the SSE endpoint for real-time article streaming.
func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Disable the server's WriteTimeout for this long-lived SSE connection.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Flush headers immediately so clients get the SSE connection established.
	flusher.Flush()

	ch := make(chan sseMessage, 16)
	h.register <- ch

	// Ensure cleanup on disconnect.
	defer func() {
		h.unregister <- ch
	}()

	ctx := r.Context()

	for {
		select {
		case <-ctx.Done():
			return

		case msg, ok := <-ch:
			if !ok {
				return
			}
			if msg.Event == "" {
				// Keepalive comment.
				fmt.Fprintf(w, ": keepalive\n\n")
			} else {
				// Guard against malformed event names that would corrupt the SSE stream.
				if strings.ContainsAny(msg.Event, "\r\n") {
					continue
				}
				fmt.Fprintf(w, "event: %s\n", msg.Event)
				// SSE data lines: each line must be prefixed with "data: ".
				for _, line := range splitLines(msg.Data) {
					fmt.Fprintf(w, "data: %s\n", line)
				}
				fmt.Fprintf(w, "\n")
			}
			flusher.Flush()
		}
	}
}

// splitLines splits a string into lines on \n, \r\n, or bare \r (per SSE spec).
func splitLines(s string) []string {
	// Normalize bare \r to \n before splitting, so \r\n becomes \n\n → handled by Split.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}
