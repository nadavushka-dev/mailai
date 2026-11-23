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

// SPEC §7a: the person sometimes answers in the agent's own session. What
// mailai can be sure of closes the item as in_session; what is only likely
// marks it; nothing new reaches the agent.

func itemByID(t *testing.T, c *client.Client, id uint64) store.Item {
	t.Helper()
	var r InboxResult
	if err := c.Call("inbox", InboxArgs{All: true}, &r); err != nil {
		t.Fatal(err)
	}
	for _, it := range r.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item #%d", id)
	return store.Item{}
}

func waitClosed(t *testing.T, c *client.Client, id uint64) store.Item {
	t.Helper()
	var it store.Item
	waitFor(t, "item closed", func() bool {
		it = itemByID(t, c, id)
		return it.ClosedAt != 0
	})
	return it
}

func rawDialAs(t *testing.T, sock string, principal store.Address) *client.Client {
	t.Helper()
	c, err := client.Dial(sock, "t-"+string(principal))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The blocking ask-human CLI died with its connection, so the item closes:
// withdrawn, since nobody typed in the session afterwards (decided
// 2026-11-23; withdraw_test.go has the in_session case).
func TestInSession_AskHumanClientGoneClosesItem(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	worker := rawDialAs(t, sock, "worker")
	go worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}}, nil)
	items := waitItems(t, person, 1)
	waitOpen(t, d, "worker", 1)
	worker.Close()
	it := waitClosed(t, person, items.Items[0].ID)
	if it.ClosedBy != store.ClosedWithdrawn || it.Answer != "" {
		t.Fatalf("%+v", it)
	}
	waitOpen(t, d, "worker", 0)
}

// An ask addressed to human is an item too, and closes the same way.
func TestInSession_AskToHumanClientGoneClosesItem(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	worker := rawDialAs(t, sock, "worker")
	go worker.Call("ask", AskArgs{To: "human", Text: "free text?"}, nil)
	items := waitItems(t, person, 1)
	worker.Close()
	if it := waitClosed(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("%+v", it)
	}
	waitOpen(t, d, "worker", 0)
}

// An ask to another agent has no item: its edge in the ask graph is
// released when the asker's connection dies, so the other agent can ask
// back without an ask_cycle.
func TestInSession_AskToAgentClientGoneReleasesEdge(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, d := startDaemon(t, pl)
	worker := rawDialAs(t, sock, "worker")
	go worker.Call("ask", AskArgs{To: "reviewer", Text: "ship?"}, nil)
	waitOpen(t, d, "worker", 1)
	worker.Close()
	waitOpen(t, d, "worker", 0)
	reviewer := dialAs(t, sock, "reviewer")
	if err := reviewer.Call("ask", AskArgs{To: "worker", Text: "?", TimeoutMS: 20}, nil); client.Code(err) != proto.CodeTimeout {
		t.Fatalf("want timeout, not a stale cycle: %v", err)
	}
}

// Only the blocking connection's death counts: a --no-wait item outlives
// its connection, and so does one whose --wait-for client goes away.
func TestInSession_NoWaitAndWaitForSurviveConnectionClose(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	w1 := rawDialAs(t, sock, "worker")
	var r AskHumanResult
	if err := w1.Call("ask-human", AskHumanArgs{Text: "Region?", NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	w1.Close()
	w2 := rawDialAs(t, sock, "worker")
	go w2.Call("ask-human", AskHumanArgs{WaitFor: r.Item.ID}, nil)
	time.Sleep(50 * time.Millisecond)
	w2.Close()
	time.Sleep(100 * time.Millisecond)
	if it := itemByID(t, person, r.Item.ID); it.ClosedAt != 0 {
		t.Fatalf("closed by a connection that was not waiting on it: %+v", it)
	}
	if err := person.Call("answer", AnswerArgs{Item: r.Item.ID, Text: "eu"}, nil); err != nil {
		t.Fatal(err)
	}
}

func holdAsync(c *client.Client, a HoldApprovalArgs) chan HoldApprovalResult {
	got := make(chan HoldApprovalResult, 1)
	go func() {
		var r HoldApprovalResult
		c.Call("hold-approval", a, &r)
		got <- r
	}()
	return got
}

// The person answered the harness's own permission dialog: the tool ran
// (or the turn ended). The item closes as in_session and the hook is let go
// with no decision, so the harness keeps what the person chose.
func TestInSession_ApprovalToolRanOrTurnEndedClosesAndReleasesHook(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofPane, Principal: "worker"}, Placement{Proof: store.ProofPane, Principal: "other"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	hook := dialAs(t, sock, "worker")
	worker := dialAs(t, sock, "worker")
	other := dialAs(t, sock, "other")
	got := holdAsync(hook, HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "git push", TimeoutMS: 5000})
	items := waitItems(t, person, 1)
	id := items.Items[0].ID
	// Another tool ran, or another agent's Bash ran: not this one.
	if err := worker.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalToolRan, Tool: "Edit"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := other.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalToolRan, Tool: "Bash"}, nil); err != nil {
		t.Fatal(err)
	}
	if it := itemByID(t, person, id); it.ClosedAt != 0 {
		t.Fatalf("closed by the wrong tool or agent: %+v", it)
	}
	if err := worker.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalToolRan, Tool: "Bash"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Decision != "" || r.Item.ClosedBy != store.ClosedInSession {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hook never released")
	}
	// The turn ended: every held approval of the agent closes.
	got = holdAsync(hook, HoldApprovalArgs{Tool: "Write", Summary: "x", TimeoutMS: 5000})
	items = waitItems(t, person, 1)
	if err := worker.Call("report-state", ReportStateArgs{State: adapter.Done, Signal: SignalTurnEnded}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Decision != "" || r.Item.ClosedBy != store.ClosedInSession {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hook never released on turn end")
	}
	// Signals come from the connection: an unknown one is refused, and the
	// person (no principal) cannot send one.
	if err := worker.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: "made-up"}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("%v", err)
	}
	if err := person.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalToolRan, Tool: "Bash"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
}

// The holding hook died (the harness gave up on it once the person
// answered the dialog): the item closes as in_session.
func TestInSession_ApprovalHookGoneClosesItem(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofPane, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	hook := rawDialAs(t, sock, "worker")
	holdAsync(hook, HoldApprovalArgs{Tool: "Bash", Summary: "x", TimeoutMS: 5000})
	items := waitItems(t, person, 1)
	hook.Close()
	if it := waitClosed(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedInSession {
		t.Fatalf("%+v", it)
	}
}

// A new prompt while an ask is open is only likely: the item is marked,
// stays open, and is still answerable (or dismissable).
func TestInSession_PromptMarksMaybeStillAnswerable(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofPane, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	sub := dial(t, sock)
	sub.Call("subscribe", nil, nil)
	var r1, r2 AskHumanResult
	worker.Call("ask-human", AskHumanArgs{Text: "Region?", Options: []string{"us", "eu"}, NoWait: true}, &r1)
	worker.Call("ask-human", AskHumanArgs{Text: "Proceed?", NoWait: true}, &r2)
	if err := worker.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalPrompt}, nil); err != nil {
		t.Fatal(err)
	}
	it := itemByID(t, person, r1.Item.ID)
	if it.MaybeInSessionAt == 0 || it.ClosedAt != 0 {
		t.Fatalf("%+v", it)
	}
	// Subscribers hear of it, without text.
	deadline := time.After(2 * time.Second)
	for heard := false; !heard; {
		select {
		case f := <-sub.Events():
			heard = f.Event == "attention" && contains(string(f.Data), `"maybe_in_session":true`)
			if contains(string(f.Data), "Region") {
				t.Fatal("event leaked the question")
			}
		case <-deadline:
			t.Fatal("no attention event for the mark")
		}
	}
	if err := person.Call("answer", AnswerArgs{Item: r1.Item.ID, Text: "eu"}, nil); err != nil {
		t.Fatal(err)
	}
	if it := itemByID(t, person, r1.Item.ID); it.ClosedBy != "answered" {
		t.Fatalf("%+v", it)
	}
	if err := person.Call("dismiss", DismissArgs{Item: r2.Item.ID}, nil); err != nil {
		t.Fatal(err)
	}
	// Nothing about it reaches the agent's context.
	var nr NoticeResult
	worker.Call("notice", NoticeArgs{}, &nr)
	if contains(nr.Line, "session") {
		t.Fatalf("%+v", nr)
	}
}

// With the Herdr adapter, blocked -> working while an ask is open is the
// same likely sign.
func TestInSession_AdapterBlockedToWorkingMarksMaybe(t *testing.T) {
	d, fa, _ := newDeliveryDaemon(t, false)
	d.st.Send(store.SendRequest{From: "worker", To: store.Human, Text: "?", Kind: store.KindAskHuman})
	d.st.Send(store.SendRequest{From: "idler", To: store.Human, Text: "?", Kind: store.KindAskHuman})
	fa.set("worker", adapter.Blocked)
	fa.set("idler", adapter.Idle)
	d.refreshAgents(context.Background())
	fa.set("worker", adapter.Working)
	fa.set("idler", adapter.Working)
	d.refreshAgents(context.Background())
	for _, it := range d.st.Items(false) {
		if (it.From == "worker") != (it.MaybeInSessionAt != 0) {
			t.Fatalf("%+v", it)
		}
	}
}

// The Inbox lists what closed in the last hour, newest first, with how;
// and every item of one thread for the Mail view's note.
func TestInSession_RecentlyClosedListing(t *testing.T) {
	clk := &testClock{t: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemonWith(t, Options{Placer: pl, Now: clk.now, Store: store.New(store.Options{Now: clk.now})})
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	ask := func(q string) uint64 {
		var r AskHumanResult
		if err := worker.Call("ask-human", AskHumanArgs{Text: q, NoWait: true}, &r); err != nil {
			t.Fatal(err)
		}
		return r.Item.ID
	}
	old, a, b, open := ask("old?"), ask("a?"), ask("b?"), ask("open?")
	person.Call("dismiss", DismissArgs{Item: old}, nil)
	clk.add(2 * time.Hour)
	person.Call("answer", AnswerArgs{Item: a, Text: "ok"}, nil)
	clk.add(time.Minute)
	gone := rawDialAs(t, sock, "worker")
	go gone.Call("ask-human", AskHumanArgs{Text: "c?"}, nil)
	var c uint64
	waitFor(t, "c open", func() bool {
		var r InboxResult
		person.Call("inbox", InboxArgs{}, &r)
		for _, it := range r.Items {
			if it.Question == "c?" {
				c = it.ID
			}
		}
		return c != 0
	})
	gone.Close()
	waitClosed(t, person, c)
	clk.add(time.Minute)
	person.Call("dismiss", DismissArgs{Item: b}, nil)

	var r InboxResult
	if err := person.Call("inbox", InboxArgs{Closed: true}, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 3 || r.Items[0].ID != b || r.Items[1].ID != c || r.Items[2].ID != a {
		t.Fatalf("%+v", r.Items)
	}
	if r.Items[0].ClosedBy != "dismissed" || r.Items[0].DecidedBy != "human" || r.Items[1].ClosedBy != store.ClosedWithdrawn || r.Items[2].ClosedBy != "answered" || r.Items[2].DecidedBy != "human" {
		t.Fatalf("%+v", r.Items)
	}
	_ = open
	th := itemByID(t, person, c).ThreadID
	if err := person.Call("inbox", InboxArgs{Thread: th}, &r); err != nil || len(r.Items) != 1 || r.Items[0].ID != c {
		t.Fatalf("%+v %v", r.Items, err)
	}
}

// ---- the star ----

// A spoke's blocking ask-human dies with its client: the spoke tells the
// inbox machine, which closes the item as in_session.
func TestStar_SpokeAskHumanClientGoneWithdrawsItem(t *testing.T) {
	s := newStar(t)
	person := dial(t, s.hubSock)
	worker := rawDialAs(t, s.spokeSock, "worker")
	go worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}}, nil)
	items := waitItems(t, person, 1)
	waitOpen(t, s.spoke, "worker", 1)
	worker.Close()
	if it := waitClosed(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedWithdrawn || it.From != "worker@s2" {
		t.Fatalf("%+v", it)
	}
	waitOpen(t, s.spoke, "worker", 0)
	// A plain ask to human from the spoke: the same.
	w3 := rawDialAs(t, s.spokeSock, "worker")
	go w3.Call("ask", AskArgs{To: "human", Text: "free text?"}, nil)
	items = waitItems(t, person, 1)
	w3.Close()
	if it := waitClosed(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedWithdrawn || it.Kind != store.ItemAsk {
		t.Fatalf("%+v", it)
	}
	// --no-wait from the spoke outlives its connection.
	w2 := rawDialAs(t, s.spokeSock, "worker")
	var r AskHumanResult
	if err := w2.Call("ask-human", AskHumanArgs{Text: "Later?", NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	w2.Close()
	time.Sleep(100 * time.Millisecond)
	if it := itemByID(t, person, r.Item.ID); it.ClosedAt != 0 {
		t.Fatalf("%+v", it)
	}
}

// A spoke's held approval: the tool ran on the spoke (or its hook died),
// so the inbox machine closes the item and the hook gets no decision.
func TestStar_SpokeApprovalToolRanAndHookGone(t *testing.T) {
	s := newStar(t)
	person := dial(t, s.hubSock)
	hook := dialAs(t, s.spokeSock, "worker")
	worker := dialAs(t, s.spokeSock, "worker")
	got := holdAsync(hook, HoldApprovalArgs{Harness: "claude-code", Tool: "Bash", Summary: "git push", TimeoutMS: 5000})
	items := waitItems(t, person, 1)
	if err := worker.Call("report-state", ReportStateArgs{State: adapter.Working, Signal: SignalToolRan, Tool: "Bash"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Decision != "" || r.Item.ClosedBy != store.ClosedInSession {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("spoke hook never released")
	}
	if it := itemByID(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedInSession {
		t.Fatalf("%+v", it)
	}
	h2 := rawDialAs(t, s.spokeSock, "worker")
	holdAsync(h2, HoldApprovalArgs{Tool: "Edit", Summary: "x", TimeoutMS: 5000})
	items = waitItems(t, person, 1)
	h2.Close()
	if it := waitClosed(t, person, items.Items[0].ID); it.ClosedBy != store.ClosedInSession {
		t.Fatalf("%+v", it)
	}
}

// A link withdraws only its own machine's agents' items; nobody else may
// use the verb.
func TestStar_LinkCannotWithdrawAnotherMachinesItem(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "reviewer"}, Placement{Proof: store.ProofDetached})
	sock, _ := startDaemonWith(t, Options{Name: "s1", Store: store.New(store.Options{MachineID: "s1"}), Placer: pl})
	person := dial(t, sock)
	l2, l3 := dial(t, sock), dial(t, sock)
	if err := l2.Call("link-hello", LinkHelloArgs{Machine: "s2"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := l3.Call("link-hello", LinkHelloArgs{Machine: "s3"}, nil); err != nil {
		t.Fatal(err)
	}
	var on3 LinkSendResult
	if err := l3.Call("link-send", LinkSendArgs{From: "worker", To: "human", Kind: store.KindAskHuman, Text: "s3's?"}, &on3); err != nil {
		t.Fatal(err)
	}
	var local AskHumanResult
	if err := dialAs(t, sock, "reviewer").Call("ask-human", AskHumanArgs{Text: "local?", NoWait: true}, &local); err != nil {
		t.Fatal(err)
	}
	for _, mid := range []string{on3.Message.MID, local.Item.MID} {
		if err := l2.Call("link-withdraw", LinkWithdrawArgs{MID: mid}, nil); client.Code(err) != proto.CodeForbidden {
			t.Fatalf("s2 withdrew %s: %v", mid, err)
		}
	}
	for _, from := range []store.Address{"", "human", "x@s3"} {
		if err := l2.Call("link-withdraw", LinkWithdrawArgs{From: from, Tool: "Bash"}, nil); client.Code(err) != proto.CodeForbidden {
			t.Fatalf("withdraw for %q: %v", from, err)
		}
	}
	// Not a link: the person, an agent, a detached caller.
	for _, c := range []*client.Client{person, dialAs(t, sock, "reviewer"), rawDialDetached(t, sock)} {
		if err := c.Call("link-withdraw", LinkWithdrawArgs{MID: on3.Message.MID}, nil); client.Code(err) != proto.CodeForbidden {
			t.Fatalf("non-link withdrew: %v", err)
		}
	}
	if it := itemByID(t, person, on3.Item.ID); it.ClosedAt != 0 {
		t.Fatalf("%+v", it)
	}
	// Its own machine's item: closed.
	if err := l3.Call("link-withdraw", LinkWithdrawArgs{MID: on3.Message.MID}, nil); err != nil {
		t.Fatal(err)
	}
	if it := itemByID(t, person, on3.Item.ID); it.ClosedBy != store.ClosedWithdrawn {
		t.Fatalf("%+v", it)
	}
}

func rawDialDetached(t *testing.T, sock string) *client.Client {
	t.Helper()
	c, err := client.Dial(sock, "t-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
