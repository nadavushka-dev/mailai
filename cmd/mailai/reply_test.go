package main

import (
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// The other party of a message, for a reply that names no --to: whoever
// sent it, or, when you sent it, whoever it went to, on any machine.
func TestReplyRecipient(t *testing.T) {
	th := []store.Message{
		{ID: 1, MID: "ubcl:1", From: "conductor", To: "lane-1@s2", ThreadID: "ubcl:1"},
		{ID: 2, MID: "s2:9", From: "lane-1@s2", To: "conductor", ThreadID: "ubcl:1"},
		{ID: 3, MID: "ubcl:3", From: "conductor", To: "lane-1@s2", ThreadID: "ubcl:1"},
		{ID: 4, MID: "ubcl:4", From: "conductor", To: "conductor", ThreadID: "ubcl:1"},
	}
	for _, c := range []struct {
		ref  string
		want store.Address
	}{{"1", "lane-1@s2"}, {"#2", "lane-1@s2"}, {"ubcl:3", "lane-1@s2"}, {"s2:9", "lane-1@s2"}, {"4", "lane-1@s2"}} {
		got, err := replyRecipient(th, c.ref, "conductor")
		if err != nil || got != c.want {
			t.Fatalf("%s: %q %v", c.ref, got, err)
		}
	}
	if _, err := replyRecipient(th, "#7", "conductor"); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Fatalf("%v", err)
	}
	if _, err := replyRecipient(th[3:], "4", "conductor"); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Fatalf("only yourself in the thread: %v", err)
	}
	// A reply to mailai's own note goes to the thread's other party.
	ask := []store.Message{
		{ID: 10, MID: "ubcl:10", Kind: store.KindAskHuman, From: "conductor", To: "human", ThreadID: "ubcl:10", SenderProof: store.ProofToken},
		{ID: 11, MID: "ubcl:11", Kind: store.KindReply, From: "mailai", To: "conductor", ThreadID: "ubcl:10"},
	}
	if got, err := replyRecipient(ask, "11", "conductor"); err != nil || got != "human" {
		t.Fatalf("reply to mailai's note: %q %v", got, err)
	}
}
