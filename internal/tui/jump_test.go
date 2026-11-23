package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// G jumps to the last row and gg to the first, as in vim and less: in the
// Inbox, in Mail, and in an open thread.
func TestJump_GAndGGInLists(t *testing.T) {
	f := person()
	f.items = nil
	for i := 1; i <= 6; i++ {
		f.items = append(f.items, store.Item{ID: uint64(i), Kind: store.ItemAskHuman, From: "worker", Question: fmt.Sprintf("q%d?", i), Options: []string{"y"}})
	}
	m := start(f)
	m = press(m, "G")
	if m.cursor != 5 {
		t.Fatalf("G: cursor %d", m.cursor)
	}
	m = press(m, "g", "g")
	if m.cursor != 0 {
		t.Fatalf("gg: cursor %d", m.cursor)
	}
	// A lone g does nothing, and another key in between cancels it.
	m = press(m, "G", "g", "k", "g")
	if m.cursor != 4 {
		t.Fatalf("g k g must not jump: cursor %d", m.cursor)
	}
	// Mail.
	f.threads = nil
	for i := 1; i <= 4; i++ {
		f.threads = append(f.threads, daemon.ThreadRow{ThreadID: fmt.Sprintf("local:%d", i), From: "worker", To: "human", Subject: fmt.Sprintf("s%d", i), Count: 1, LastFrom: "worker"})
	}
	m = start(f)
	m = press(m, "m", "G")
	if m.cursor != 3 {
		t.Fatalf("Mail G: cursor %d", m.cursor)
	}
	m = press(m, "g", "g")
	if m.cursor != 0 {
		t.Fatalf("Mail gg: cursor %d", m.cursor)
	}
	// Nothing to jump in: no panic, cursor stays 0.
	f.items = nil
	m = press(start(f), "G", "g", "g")
	if m.cursor != 0 {
		t.Fatalf("empty: %d", m.cursor)
	}
}

func TestJump_GAndGGInAThread(t *testing.T) {
	f := person()
	var long []string
	for i := 1; i <= 80; i++ {
		long = append(long, fmt.Sprintf("line %d", i))
	}
	f.thread["local:1"] = []store.Message{{ID: 1, MID: "local:1", From: "worker", To: "human", Text: strings.Join(long, "\n"), SenderProof: store.ProofPane}}
	m := start(f)
	m = press(m, "m", "enter")
	if m.open == nil {
		t.Fatal("thread did not open")
	}
	if v := m.View(); !strings.Contains(v, "line 1 ") && !strings.Contains(v, "line 1\n") && !strings.Contains(v, "│ line 1") {
		t.Fatalf("top: %s", v)
	}
	m = press(m, "G")
	if v := m.View(); !strings.Contains(v, "line 80") {
		t.Fatalf("G should show the end: %s", v)
	}
	// One k from the bottom moves one line up, not from some far offset.
	bottom := m.openTop
	m = press(m, "k")
	if m.openTop != bottom-1 {
		t.Fatalf("k after G: %d, want %d", m.openTop, bottom-1)
	}
	// j stops at the end instead of counting past it.
	m = press(m, "j", "j", "j", "j")
	if m.openTop != bottom {
		t.Fatalf("j past the end: %d, want %d", m.openTop, bottom)
	}
	m = press(m, "g", "g")
	if m.openTop != 0 || !strings.Contains(m.View(), "│ line 1") {
		t.Fatalf("gg: %d", m.openTop)
	}
}
