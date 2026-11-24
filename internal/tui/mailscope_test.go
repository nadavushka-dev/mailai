package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// agentMail is a person's Mail with one thread to the person and one
// between two agents.
func agentMail() *fake {
	f := person()
	f.threads = []daemon.ThreadRow{
		{ThreadID: "local:1", From: "worker", To: "human", Subject: "deploy", Count: 1, Unread: 1, UnreadMine: 1, LastFrom: "worker", People: []store.Address{"human", "worker"}},
		{ThreadID: "s2:4", From: "migrator@s2", To: "worker", Subject: "schema", Count: 1, Unread: 1, LastFrom: "migrator@s2", People: []store.Address{"migrator@s2", "worker"}},
	}
	f.thread["s2:4"] = []store.Message{{ID: 9, MID: "s2:4", From: "migrator@s2", To: "worker", Text: "0042 adds refunds.reason"}}
	return f
}

// D13: Mail splits into what is to the person and what is between agents;
// t cycles every thread → to me → between agents.
func TestMailScopes(t *testing.T) {
	m := start(agentMail())
	m = press(m, "m")
	if v := m.View(); !strings.Contains(v, "deploy") || !strings.Contains(v, "schema") {
		t.Fatalf("every thread: %s", v)
	}
	m = press(m, "t")
	if v := m.View(); !strings.Contains(v, "Mail · to me") || !strings.Contains(v, "deploy") || strings.Contains(v, "schema") {
		t.Fatalf("to me: %s", v)
	}
	m = press(m, "t")
	if v := m.View(); !strings.Contains(v, "Mail · between agents") || strings.Contains(v, "deploy") || !strings.Contains(v, "schema") {
		t.Fatalf("between agents: %s", v)
	}
	m = press(m, "t")
	if v := m.View(); !strings.Contains(v, "deploy") || !strings.Contains(v, "schema") {
		t.Fatalf("back to every thread: %s", v)
	}
}

// Following one agent: the principal filter covers every thread the agent
// is in, including ones it joined after the first message.
func TestMailFilterFollowsAnAgentThroughPeople(t *testing.T) {
	f := agentMail()
	f.threads = append(f.threads, daemon.ThreadRow{ThreadID: "local:7", From: "reviewer", To: "gemini", Subject: "cap", Count: 3, LastFrom: "gemini",
		People: []store.Address{"gemini", "reviewer", "worker"}})
	m := start(f)
	m = press(m, "m", "f", "w", "o", "r", "k", "e", "r", "enter")
	v := m.View()
	for _, want := range []string{"deploy", "schema", "cap"} {
		if !strings.Contains(v, want) {
			t.Fatalf("following worker misses %q: %s", want, v)
		}
	}
}

// The person stepping into a thread between agents writes to both of them
// by default; tab picks one.
func TestJoinAgentThreadRepliesToBothOrOne(t *testing.T) {
	f := agentMail()
	m := start(f)
	m = press(m, "m", "t", "t", "r")
	if v := m.View(); !strings.Contains(v, "join thread as you → migrator@s2 + worker") {
		t.Fatalf("%s", v)
	}
	m = press(m, "o", "k", "enter")
	if fmt.Sprint(f.replied) != "[s2:4 migrator@s2 ok s2:4 worker ok]" {
		t.Fatalf("%v", f.replied)
	}
	f.replied = nil
	m = press(m, "r", "tab")
	if v := m.View(); !strings.Contains(v, "join thread as you → migrator@s2") || strings.Contains(v, "+ worker") {
		t.Fatalf("after tab: %s", v)
	}
	m = press(m, "tab")
	if v := m.View(); !strings.Contains(v, "join thread as you → worker") {
		t.Fatalf("after second tab: %s", v)
	}
	m = press(m, "n", "o", "enter")
	if fmt.Sprint(f.replied) != "[s2:4 worker no]" {
		t.Fatalf("%v", f.replied)
	}
	// A thread with the person keeps the plain reply.
	f.replied = nil
	m = press(m, "esc", "t", "r")
	if v := m.View(); !strings.Contains(v, "reply to worker") {
		t.Fatalf("%s", v)
	}
	press(m, "o", "k", "enter")
	if fmt.Sprint(f.replied) != "[local:1 worker ok]" {
		t.Fatalf("%v", f.replied)
	}
}

// Opening a thread marks the person's own unread mail in it read, never an
// agent's; inside a pane (browse only) nothing is marked.
func TestOpeningAThreadMarksThePersonsMailRead(t *testing.T) {
	f := agentMail()
	m := start(f)
	m = press(m, "m", "enter")
	if fmt.Sprint(f.marked) != "[[1]]" {
		t.Fatalf("marked %v", f.marked)
	}
	f.marked = nil
	m = press(m, "esc", "j", "enter")
	if len(f.marked) != 0 {
		t.Fatalf("marked an agent's mail: %v", f.marked)
	}
	// The person's own unread shows solid; an agent's unread carries no
	// mark for the person (SPEC §12: mail between agents is quiet).
	m = press(m, "esc")
	v := m.View()
	if !strings.Contains(v, "●") || strings.Contains(v, "○") {
		t.Fatalf("%s", v)
	}

	g := agentMail()
	g.who = daemon.WhoamiResult{Principal: "w2Y:pY", Proof: store.ProofPane}
	press(start(g), "m", "enter")
	if len(g.marked) != 0 {
		t.Fatalf("browse-only client marked %v", g.marked)
	}
}

// SPEC §12: only mail to or from the person counts as new. The unread
// filter and the detail line count the person's own unread, never a
// thread where only an agent has unread mail.
func TestUnreadCountsOnlyThePersonsMail(t *testing.T) {
	m := start(agentMail())
	m = press(m, "m", "u")
	if v := m.View(); !strings.Contains(v, "deploy") || strings.Contains(v, "schema") {
		t.Fatalf("unread filter: %s", v)
	}
	m = press(m, "u", "j")
	if v := m.View(); !strings.Contains(v, "1 messages, 0 unread") {
		t.Fatalf("detail of a thread only an agent has unread: %s", v)
	}
}

// SPEC §6: on the Between agents scope, X deletes every read thread between
// agents, after a y; the question says how many.
func TestMailDeleteReadBetweenAgentsAsksFirst(t *testing.T) {
	f := agentMail()
	f.threads = append(f.threads, daemon.ThreadRow{ThreadID: "local:7", From: "reviewer", To: "worker", Subject: "nit", Count: 2, LastFrom: "reviewer", People: []store.Address{"reviewer", "worker"}})
	m := start(f)
	if m = press(m, "m", "t", "X"); f.bulkDeleted != 0 || m.confirmBulk {
		t.Fatal("X outside the Between agents scope did something")
	}
	m = press(m, "t")
	m = press(m, "X")
	if v := m.View(); !strings.Contains(v, "delete 1 read thread between agents (2 messages)") || !strings.Contains(v, "y deletes") {
		t.Fatalf("no question:\n%s", v)
	}
	if m = press(m, "n"); f.bulkDeleted != 0 || m.confirmBulk {
		t.Fatal("any key but y keeps them")
	}
	if v := m.View(); !strings.Contains(v, "delete read") {
		t.Fatalf("footer does not name X:\n%s", v)
	}
	press(m, "X", "y")
	if f.bulkDeleted != 1 {
		t.Fatalf("bulk deletes %d", f.bulkDeleted)
	}
}
