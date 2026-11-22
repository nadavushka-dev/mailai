package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// An item the person answered outside mailai (closed in_session) carries
// the "answered in session" badge wherever it shows, as the maybe badge does
// for an open one (SPEC §7a).
func TestInSession_AnsweredBadge(t *testing.T) {
	f := person()
	closed := store.Item{ID: 7, Kind: store.ItemAskHuman, From: "worker", Question: "Merge it?", ThreadID: "local:7",
		OpenedAt: ns(3 * time.Minute), ClosedAt: ns(time.Minute), ClosedBy: store.ClosedInSession}
	f.recent = []store.Item{closed}
	m := wide(start(f))
	if line := lineWith(m.View(), "Merge it?"); !strings.Contains(line, daemon.InSessionBadge) {
		t.Fatalf("strip: want %q in %q", daemon.InSessionBadge, line)
	}
	// In the A (all) list too.
	f.items = append(f.items, closed)
	m = wide(start(f))
	m = press(m, "A")
	if line := lineWith(m.View(), "Merge it?"); !strings.Contains(line, daemon.InSessionBadge) {
		t.Fatalf("all list: want %q in %q", daemon.InSessionBadge, line)
	}
}
