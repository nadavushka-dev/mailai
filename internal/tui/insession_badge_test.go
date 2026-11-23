package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// A question its agent took back says so, and by whom, in the strip, the
// detail and its thread; never "by human" (SPEC §7a, 2026-11-23).
func TestWithdrawnSaysWhoTookItBack(t *testing.T) {
	f := person()
	w := store.Item{ID: 7, Kind: store.ItemAskHuman, From: "builder", Question: "Layout ok?", ThreadID: "local:1",
		OpenedAt: ns(3 * time.Minute), ClosedAt: ns(time.Minute), ClosedBy: store.ClosedWithdrawn}
	f.recent = []store.Item{w}
	m := wide(start(f))
	if line := lineWith(m.View(), "Layout ok?"); !strings.Contains(line, "withdrawn by builder") {
		t.Fatalf("strip: %q", line)
	}
	f.items = append(f.items, w)
	m = press(wide(start(f)), "A")
	for m.cursor < len(m.items) && m.items[m.cursor].ID != 7 {
		m = press(m, "j")
	}
	v := m.View()
	if !strings.Contains(v, "withdrawn by builder at") || strings.Contains(v, "withdrawn by human") {
		t.Fatalf("detail: %s", v)
	}
	// The thread's note.
	f.byThread = map[string][]store.Item{"local:1": {w}}
	m = press(wide(start(f)), "m", "enter")
	if !strings.Contains(m.View(), "#7 withdrawn by builder at") {
		t.Fatalf("thread: %s", m.View())
	}
}

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
