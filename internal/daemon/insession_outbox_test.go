package daemon

import (
	"testing"
	"time"
)

// §7a with the outbox (§13a): an ask-human queued on a spoke while the link
// is down, whose asker then goes away (the person answered in the pane),
// must never reach the inbox machine as a fresh question nobody waits on.
func TestStar_QueuedAskHumanWithdrawnBeforeItLeaves(t *testing.T) {
	s := newStar(t)
	person := dial(t, s.hubSock)
	s.cut(true)
	s.waitLink(t, false)
	worker := rawDialAs(t, s.spokeSock, "worker")
	go worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}}, nil)
	waitFor(t, "queued on the spoke", func() bool { return len(s.spoke.st.Outbox()) == 1 })
	worker.Close()
	waitFor(t, "dropped from the outbox", func() bool { return len(s.spoke.st.Outbox()) == 0 })
	s.heal()
	s.waitLink(t, true)
	time.Sleep(200 * time.Millisecond)
	var inbox InboxResult
	if err := person.Call("inbox", InboxArgs{}, &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Items) != 0 {
		t.Fatalf("a withdrawn queued question opened an item: %+v", inbox.Items)
	}
}
