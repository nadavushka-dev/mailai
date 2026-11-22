package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Found in the sandbox: SIGINT (or a service stop) never ended the daemon
// while an agent was blocked on ask-human or a hook held an approval, since
// Serve waited for every connection. Stopping must end Serve promptly, and
// must not read the asker's connection closing as "answered in the
// session" (§7a): the items stay open for the next daemon.
func TestShutdownWithBlockedClientsEndsAndKeepsItemsOpen(t *testing.T) {
	st := store.New(store.Options{})
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	d := New(Options{Store: st, Placer: pl})
	sock := shortSock(t)
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()

	asker := rawDialAs(t, sock, "worker")
	go asker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}}, nil)
	hook := rawDialAs(t, sock, "worker")
	go hook.Call("hold-approval", HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "ls"}, nil)
	waitFor(t, "two open items", func() bool { return len(st.Items(false)) == 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return with blocked clients connected")
	}
	for _, it := range st.Items(true) {
		if it.ClosedBy == store.ClosedInSession {
			t.Fatalf("shutdown closed #%d as in_session", it.ID)
		}
	}
}

// Also found in the sandbox: a link that dropped while the inbox machine
// held its approval leaves that connection waiting on the hold (a dropped
// link is not the person answering, so the hold runs to its timeout). A
// shutdown meanwhile must not wait for it.
func TestShutdownWithAHoldFromADroppedLinkEnds(t *testing.T) {
	st := store.New(store.Options{MachineID: "s1"})
	d := New(Options{Name: "s1", Store: st, Placer: placed(Placement{Proof: store.ProofDetached})})
	sock := shortSock(t)
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()

	link := dial(t, sock)
	if err := link.Call("link-hello", LinkHelloArgs{Machine: "s2"}, nil); err != nil {
		t.Fatal(err)
	}
	go link.Call("link-hold", LinkHoldArgs{From: "worker", Hold: HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "ls", TimeoutMS: 60000}}, nil)
	waitFor(t, "the held item", func() bool { return len(st.Items(false)) == 1 })
	link.Close()
	time.Sleep(100 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve waited for a hold whose link had dropped")
	}
}
