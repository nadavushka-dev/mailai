package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Decided 2026-11-23 (SPEC §7a), found on UbCl: an agent that stopped its
// own waiting ask-human (to re-ask with a file) was recorded as "answered
// in the session". A dropped ask is now withdrawn; it becomes in_session
// only when the person then types in that agent's session within the
// window. Agents take a question back with withdraw, or ask-human
// --replaces.

// The agent's blocking ask died and nobody typed afterwards: withdrawn.
func TestWithdraw_DroppedAskWithNoTypingIsWithdrawn(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	worker := rawDialAs(t, sock, "worker")
	go worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}}, nil)
	items := waitItems(t, person, 1)
	waitOpen(t, d, "worker", 1)
	// The person typed BEFORE the drop (the agent then cancelled itself).
	w2 := rawDialAs(t, sock, "worker")
	defer w2.Close()
	if err := w2.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalPrompt}, nil); err != nil {
		t.Fatal(err)
	}
	worker.Close()
	it := waitClosed(t, person, items.Items[0].ID)
	if it.ClosedBy != store.ClosedWithdrawn || it.Answer != "" {
		t.Fatalf("%+v", it)
	}
	if got := ClosedHow(it); got != "withdrawn by worker" {
		t.Fatalf("ClosedHow %q", got)
	}
	waitOpen(t, d, "worker", 0)
}

// Esc in the harness: the ask dies first, then the person types in that
// session (a prompt from the agent's hook): answered in the session.
func TestWithdraw_TypingAfterTheDropMeansAnsweredInSession(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "other"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := rawDialAs(t, sock, "worker")
	go worker.Call("ask-human", AskHumanArgs{Text: "Deploy?"}, nil)
	items := waitItems(t, person, 1)
	id := items.Items[0].ID
	worker.Close()
	if it := waitClosed(t, person, id); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("%+v", it)
	}
	// Another agent's prompt says nothing about this one.
	other := rawDialAs(t, sock, "other")
	defer other.Close()
	other.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalPrompt}, nil)
	if it := itemByID(t, person, id); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("another agent's prompt relabelled it: %+v", it)
	}
	w2 := rawDialAs(t, sock, "worker")
	defer w2.Close()
	if err := w2.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalPrompt}, nil); err != nil {
		t.Fatal(err)
	}
	if it := itemByID(t, person, id); it.ClosedBy != store.ClosedInSession {
		t.Fatalf("%+v", it)
	}
}

// Typing long after the drop is not about the question.
func TestWithdraw_TypingAfterTheWindowChangesNothing(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemonWith(t, Options{Placer: pl, InSessionWindow: 50 * time.Millisecond})
	person := dial(t, sock)
	worker := rawDialAs(t, sock, "worker")
	go worker.Call("ask-human", AskHumanArgs{Text: "Deploy?"}, nil)
	items := waitItems(t, person, 1)
	worker.Close()
	waitClosed(t, person, items.Items[0].ID)
	time.Sleep(120 * time.Millisecond)
	w2 := rawDialAs(t, sock, "worker")
	defer w2.Close()
	w2.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalPrompt}, nil)
	if it := itemByID(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("%+v", it)
	}
}

// With Herdr and no hooks: after the drop the agent came to rest and then
// worked again (the person typed). Working all along (it cancelled itself
// mid-turn) stays withdrawn.
func TestWithdraw_AdapterRestThenWorkingAfterTheDrop(t *testing.T) {
	d, fa, _ := newDeliveryDaemon(t, false)
	ask := func(from store.Address) uint64 {
		m, err := d.st.Send(store.SendRequest{From: from, To: store.Human, Text: "?", Kind: store.KindAskHuman})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range d.st.Items(false) {
			if it.MID == m.MID {
				return it.ID
			}
		}
		t.Fatal("no item")
		return 0
	}
	esc, busy := ask("worker"), ask("busy")
	fa.set("worker", adapter.Working)
	fa.set("busy", adapter.Working)
	d.refreshAgents(context.Background())
	d.askDropped(esc, "worker")
	d.askDropped(busy, "busy")
	fa.set("worker", adapter.Idle)
	d.refreshAgents(context.Background())
	fa.set("worker", adapter.Working)
	d.refreshAgents(context.Background())
	d.refreshAgents(context.Background())
	if it, _ := d.st.Item(esc); it.ClosedBy != store.ClosedInSession {
		t.Fatalf("rest then working: %+v", it)
	}
	if it, _ := d.st.Item(busy); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("working all along: %+v", it)
	}
}

// withdraw: an agent takes back its own open question, and only its own.
func TestWithdraw_VerbOnlyForTheAsker(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
	var r AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Ship?", NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	if err := reviewer.Call("withdraw", WithdrawArgs{Item: r.Item.ID}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("another agent: %v", err)
	}
	if err := person.Call("withdraw", WithdrawArgs{Item: r.Item.ID}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("the person dismisses, not withdraws: %v", err)
	}
	var w WithdrawResult
	if err := worker.Call("withdraw", WithdrawArgs{Item: r.Item.ID}, &w); err != nil {
		t.Fatal(err)
	}
	if w.Item.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("%+v", w)
	}
	if err := worker.Call("withdraw", WithdrawArgs{Item: r.Item.ID}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("twice: %v", err)
	}
	// Withdrawn means no answer can land on it.
	if err := person.Call("answer", AnswerArgs{Item: r.Item.ID, Text: "yes"}, nil); err == nil {
		t.Fatal("answered a withdrawn question")
	}
}

// A blocking ask-human still waiting on a withdrawn question ends, and its
// ask slot is freed.
func TestWithdraw_EndsTheWaitingAsk(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	waiter := rawDialAs(t, sock, "worker")
	defer waiter.Close()
	errc := make(chan error, 1)
	go func() { errc <- waiter.Call("ask-human", AskHumanArgs{Text: "Deploy?"}, nil) }()
	items := waitItems(t, person, 1)
	waitOpen(t, d, "worker", 1)
	if err := dialAs(t, sock, "worker").Call("withdraw", WithdrawArgs{Item: items.Items[0].ID}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if client.Code(err) != proto.CodeNotFound || !contains(err.Error(), "withdrawn") {
			t.Fatalf("%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting ask-human never ended")
	}
	waitOpen(t, d, "worker", 0)
	if it := itemByID(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("%+v", it)
	}
}

// ask-human --replaces N: the new question opens and N is withdrawn, in
// one step; N must be the asker's own open question.
func TestWithdraw_AskHumanReplaces(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	var old, theirs, repl AskHumanResult
	worker.Call("ask-human", AskHumanArgs{Text: "Layout ok?", NoWait: true}, &old)
	dialAs(t, sock, "reviewer").Call("ask-human", AskHumanArgs{Text: "Mine?", NoWait: true}, &theirs)
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Layout ok? (file attached)", NoWait: true, Replaces: theirs.Item.ID}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("replacing another agent's question: %v", err)
	}
	if n := len(waitItems(t, person, 2).Items); n != 2 {
		t.Fatalf("a refused replace still opened a question: %d open", n)
	}
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Layout ok? (file attached)", NoWait: true, Replaces: old.Item.ID}, &repl); err != nil {
		t.Fatal(err)
	}
	if it := itemByID(t, person, old.Item.ID); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("old: %+v", it)
	}
	if it := itemByID(t, person, repl.Item.ID); it.ClosedAt != 0 {
		t.Fatalf("new: %+v", it)
	}
}

// Items of a spoke's agents live on the inbox machine; withdrawing them
// from a spoke comes later (v0.1), and says so.
func TestWithdraw_FromASpokeSaysNotYet(t *testing.T) {
	s := newStar(t)
	w := rawDialAs(t, s.spokeSock, "worker")
	defer w.Close()
	err := w.Call("withdraw", WithdrawArgs{Item: 1}, nil)
	if client.Code(err) != proto.CodeBadRequest || !contains(err.Error(), "spoke") {
		t.Fatalf("%v", err)
	}
}
