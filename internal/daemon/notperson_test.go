package daemon

import (
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Found on UbCl: the TUI opened in a Herdr pane said "inbox empty: nothing
// waits on you" while the person's Inbox held a question. A caller that is
// not the person is told so, and why, on whoami and on inbox.
func TestNotThePerson_SaysWhy(t *testing.T) {
	sock, _ := startDaemonWith(t, Options{Placer: &fakePlacer{byToken: map[string]Placement{
		"pane":     {Proof: store.ProofPane, Principal: "w4Y:p1"},
		"agent":    {Proof: store.ProofAgent, Agent: "claude"},
		"detached": {Proof: store.ProofDetached},
		"token":    {Proof: store.ProofToken, Principal: "builder"},
	}}})
	dial(t, sock).Call("ask-human", AskHumanArgs{From: "worker", Text: "ok?", NoWait: true}, nil)
	want := map[string][]string{
		"pane":     {"w4Y:p1", "agent", "mailai web"},
		"agent":    {"claude", "agent", "mailai web"},
		"detached": {"no terminal", "ssh -t", "mailai web"},
		"token":    {"builder", "MAILAI_TOKEN"},
	}
	for tok, words := range want {
		c, err := client.Dial(sock, tok)
		if err != nil {
			t.Fatal(err)
		}
		var who WhoamiResult
		c.Call("whoami", nil, &who)
		var in InboxResult
		c.Call("inbox", InboxArgs{}, &in)
		c.Close()
		for _, w := range words {
			if !strings.Contains(who.NotPerson, w) {
				t.Errorf("%s: whoami says %q, lacks %q", tok, who.NotPerson, w)
			}
		}
		if in.NotPerson != who.NotPerson || len(in.Items) != 0 {
			t.Errorf("%s: inbox %+v", tok, in)
		}
	}
	var who WhoamiResult
	dial(t, sock).Call("whoami", nil, &who)
	var in InboxResult
	dial(t, sock).Call("inbox", InboxArgs{}, &in)
	if who.NotPerson != "" || in.NotPerson != "" || len(in.Items) != 1 {
		t.Fatalf("the person: %+v %+v", who, in)
	}
}
