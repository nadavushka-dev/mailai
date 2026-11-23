package tui

import (
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §9: a pane-proven sender's pane is shown beside its name, so a name
// that moved to another pane is visible; never twice when the name is the
// pane id.
func TestSendersPaneBesideTheName(t *testing.T) {
	f := person()
	f.threads = []daemon.ThreadRow{{ThreadID: "local:1", From: "builder", FromPane: "w4Y:p1", To: "human", Subject: "deploy",
		Count: 2, Unread: 1, LastFrom: "builder", LastFromPane: "w4Y:p1"}}
	f.thread = map[string][]store.Message{"local:1": {
		{ID: 1, MID: "local:1", From: "builder", To: "human", Text: "Deploy?", SenderProof: store.ProofPane, SenderPane: "w4Y:p1"},
		{ID: 2, MID: "local:2", From: "w2Y:p1", To: "human", Text: "me too", SenderProof: store.ProofPane, SenderPane: "w2Y:p1"},
	}}
	m := wide(start(f))
	m = press(m, "m")
	if row := lineWith(m.View(), "deploy"); !strings.Contains(row, "builder · w4Y:p1") {
		t.Fatalf("list row: %q", row)
	}
	m = press(m, "enter")
	v := m.View()
	if !strings.Contains(v, "#1 builder · w4Y:p1 pane") {
		t.Fatalf("thread head:\n%s", v)
	}
	if strings.Contains(v, "w2Y:p1 · w2Y:p1") || !strings.Contains(v, "#2 w2Y:p1 pane") {
		t.Fatalf("an unnamed pane is shown once:\n%s", v)
	}
}
