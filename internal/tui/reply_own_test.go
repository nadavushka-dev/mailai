package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Found on UbCl (2026-11-24): a reply to one's own message went back to
// oneself. A thread whose messages are all the person's (nobody answered
// yet) replies to the agent the person wrote to, not to human; mailai's
// own note in a thread is nobody to reply to.
func TestReplyInAThreadOfYourOwnGoesToTheOtherParty(t *testing.T) {
	f := person()
	f.threads = []daemon.ThreadRow{{ThreadID: "local:5", From: "human", To: "builder", Subject: "plan", Count: 2, LastFrom: "human"}}
	f.thread = map[string][]store.Message{"local:5": {
		{ID: 5, MID: "local:5", From: "human", To: "builder", Text: "plan the release", ThreadID: "local:5", SenderProof: store.ProofOutsidePane, Verified: "human"},
		{ID: 6, MID: "local:6", Kind: store.KindReply, From: "human", To: "builder", ReplyTo: "local:5", Text: "and the notes", ThreadID: "local:5", SenderProof: store.ProofOutsidePane, Verified: "human"},
	}}
	m := press(start(f), "m", "enter", "r")
	if v := m.View(); !strings.Contains(v, "reply to builder") {
		t.Fatalf("%s", v)
	}
	press(m, "o", "k", "enter")
	if fmt.Sprint(f.replied) != "[local:6 builder ok]" {
		t.Fatalf("%v", f.replied)
	}
	// An ask whose thread ends with mailai's note: the reply goes to the
	// asker, not to mailai.
	f.replied = nil
	f.thread["local:5"] = []store.Message{
		{ID: 7, MID: "local:7", Kind: store.KindAskHuman, From: "builder", To: "human", Text: "Ship?", ThreadID: "local:5", SenderProof: store.ProofPane},
		{ID: 8, MID: "local:8", Kind: store.KindReply, From: daemon.Bouncer, To: "builder", ReplyTo: "local:7", Text: "#3 dismissed by the person", ThreadID: "local:5"},
	}
	m = press(start(f), "m", "enter", "r")
	press(m, "o", "k", "enter")
	if fmt.Sprint(f.replied) != "[local:7 builder ok]" {
		t.Fatalf("%v", f.replied)
	}
}
