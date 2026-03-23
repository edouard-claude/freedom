package mistral

import (
	"net/http"
	"sync"
	"time"
)

// ThrottledTransport wraps an http.RoundTripper and enforces a minimum
// interval between requests. Safe for concurrent use.
type ThrottledTransport struct {
	base     http.RoundTripper
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

// NewThrottledTransport returns a transport that allows at most one request
// per interval across all goroutines sharing it.
func NewThrottledTransport(interval time.Duration) *ThrottledTransport {
	return &ThrottledTransport{
		base:     http.DefaultTransport,
		interval: interval,
	}
}

// RoundTrip implements http.RoundTripper with rate limiting.
func (t *ThrottledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for {
		t.mu.Lock()
		wait := t.interval - time.Since(t.last)
		if wait <= 0 {
			t.last = time.Now()
			t.mu.Unlock()
			return t.base.RoundTrip(req)
		}
		t.mu.Unlock()

		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
}
