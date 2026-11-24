package tui

import (
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §6/§12 (decided 2026-11-24, replacing X): in Mail, space or x marks
// the row (unread ones too); D on marked rows asks once, naming the count,
// and y deletes every marked thread. Esc clears the marks.
func TestMailMarksAndDeletesMarked(t *testing.T) {
	f := agentMail()
	f.threads = append(f.threads, daemon.ThreadRow{ThreadID: "local:7", From: "reviewer", To: "worker", Subject: "nit", Count: 2, LastFrom: "reviewer", People: []store.Address{"reviewer", "worker"}})
	m := start(f)
	if m = press(m, " "); len(m.marked) != 0 {
		t.Fatal("space in the Inbox marked something")
	}
	m = press(m, "m")
	if v := m.View(); !strings.Contains(v, "space mark") || strings.Contains(v, "delete read") {
		t.Fatalf("footer:\n%s", v)
	}
	m = press(m, " ", "j", "x")
	if !m.marked["local:1"] || !m.marked["s2:4"] || len(m.marked) != 2 {
		t.Fatalf("marked %v", m.marked)
	}
	if m = press(m, "x"); m.marked["s2:4"] || len(m.marked) != 1 {
		t.Fatalf("x again unmarks: %v", m.marked)
	}
	m = press(m, "j", " ")
	if v := m.View(); !strings.Contains(v, "2 marked") || !strings.Contains(v, "D delete marked") {
		t.Fatalf("no count or hint:\n%s", v)
	}
	m = press(m, "D")
	if v := m.View(); !strings.Contains(v, "delete 2 marked threads (3 messages)? y deletes") {
		t.Fatalf("no question:\n%s", v)
	}
	if m = press(m, "n"); len(f.deleted) != 0 || len(m.marked) != 2 {
		t.Fatalf("any key but y keeps them: %v %v", f.deleted, m.marked)
	}
	m = press(m, "D", "y")
	if strings.Join(f.deleted, " ") != "local:1 local:7" || len(m.marked) != 0 {
		t.Fatalf("deleted %v, still marked %v", f.deleted, m.marked)
	}
	// Nothing marked: D is the one thread under the cursor, as before.
	if v := press(m, "D").View(); !strings.Contains(v, "delete thread") {
		t.Fatalf("D with nothing marked:\n%s", v)
	}
	m = press(m, " ")
	if m = press(m, "esc"); len(m.marked) != 0 {
		t.Fatalf("esc keeps %v", m.marked)
	}
}

// Marks survive live updates and go with threads that leave the list.
func TestMailMarksFollowTheList(t *testing.T) {
	f := agentMail()
	m := start(f)
	m = press(m, "m", " ", "j", " ")
	if len(m.marked) != 2 {
		t.Fatalf("marked %v", m.marked)
	}
	// A new thread arrives; the marks stay.
	f.threads = append([]daemon.ThreadRow{{ThreadID: "local:9", From: "worker", To: "human", Subject: "new", Count: 1, LastFrom: "worker"}}, f.threads...)
	m = drain(m, m.refresh())
	if len(m.marked) != 2 {
		t.Fatalf("a new thread dropped marks: %v", m.marked)
	}
	// One goes (deleted elsewhere): its mark goes with it.
	f.threads = f.threads[:2]
	m = drain(m, m.refresh())
	if !m.marked["local:1"] || len(m.marked) != 1 {
		t.Fatalf("marked %v", m.marked)
	}
	// One no longer shown (another scope) is no longer marked: D never
	// deletes what is out of sight.
	m = press(m, "t", "t")
	if len(m.marked) != 0 {
		t.Fatalf("hidden but marked %v", m.marked)
	}
}
