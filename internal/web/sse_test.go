package web

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func newTestHub(t *testing.T) (*SSEHub, context.CancelFunc) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewSSEHub(logger)
	ctx, cancel := context.WithCancel(context.Background())
	go hub.Run(ctx)
	return hub, cancel
}

func registerClient(t *testing.T, hub *SSEHub) chan sseMessage {
	t.Helper()
	ch := make(chan sseMessage, 16)
	hub.register <- ch
	return ch
}

func unregisterClient(t *testing.T, hub *SSEHub, ch chan sseMessage) {
	t.Helper()
	hub.unregister <- ch
}

// waitHubSync ensures the hub has processed all pending events by round-tripping
// through the synchronous ClientCount() call.
func waitHubSync(hub *SSEHub) {
	hub.ClientCount()
}

func TestPresenceWakeOnFirstClient(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	ch := registerClient(t, hub)
	defer unregisterClient(t, hub, ch)

	select {
	case wake := <-hub.Presence():
		if !wake {
			t.Fatal("expected wake=true on first client, got false")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for presence wake signal")
	}
}

func TestPresenceSleepOnLastClientDisconnect(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	ch1 := registerClient(t, hub)
	<-hub.Presence() // consume wake

	ch2 := registerClient(t, hub)

	// Unregister first client — should NOT trigger sleep (1 client remaining).
	unregisterClient(t, hub, ch1)
	waitHubSync(hub) // ensure hub processed the unregister

	// Check no signal.
	select {
	case sig := <-hub.Presence():
		t.Fatalf("unexpected presence signal %v when 1 client still connected", sig)
	default:
	}

	// Unregister last client — should trigger sleep.
	unregisterClient(t, hub, ch2)

	select {
	case sleep := <-hub.Presence():
		if sleep {
			t.Fatal("expected sleep=false on last client disconnect, got true")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for presence sleep signal")
	}
}

func TestPresenceNoSignalOnNonZeroTransitions(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	ch1 := registerClient(t, hub)
	<-hub.Presence() // consume wake for 0→1

	ch2 := registerClient(t, hub) // 1→2 — no signal expected
	waitHubSync(hub)              // ensure hub processed the register

	select {
	case sig := <-hub.Presence():
		t.Fatalf("unexpected presence signal %v on 1→2 transition", sig)
	default:
	}

	unregisterClient(t, hub, ch1) // 2→1 — no signal expected
	waitHubSync(hub)              // ensure hub processed the unregister

	select {
	case sig := <-hub.Presence():
		t.Fatalf("unexpected presence signal %v on 2→1 transition", sig)
	default:
	}

	unregisterClient(t, hub, ch2)
}

func TestNotifyStatusBroadcast(t *testing.T) {
	hub, cancel := newTestHub(t)
	defer cancel()

	ch := registerClient(t, hub)
	<-hub.Presence() // consume wake

	hub.NotifyStatus("Initialisation du pipeline...")

	select {
	case msg := <-ch:
		if msg.Event != "status" {
			t.Fatalf("expected event 'status', got %q", msg.Event)
		}
		if msg.Data != "Initialisation du pipeline..." {
			t.Fatalf("expected data 'Initialisation du pipeline...', got %q", msg.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for status message")
	}

	unregisterClient(t, hub, ch)
}
