package daemon

import (
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

func supervisorSetup(t *testing.T) (sock string, d *Daemon) {
	t.Helper()
	pl := placed(
		Placement{Proof: store.ProofToken, Principal: "sup"},
		Placement{Proof: store.ProofToken, Principal: "sub"},
		Placement{Proof: store.ProofPane, Principal: "w1"},
		Placement{Proof: store.ProofPane, Principal: "w3"},
	)
	// Sessions: w1, w2 in api; w3 in web.
	fa := &fakeAdapter{states: map[store.Address]adapter.State{"w1": adapter.Idle, "w2": adapter.Idle, "w3": adapter.Idle},
		sessions: map[store.Address]string{"w1": "api", "w2": "api", "w3": "web"}}
	sock, d = startDaemonWith(t, Options{Placer: pl, Adapter: fa, PollEvery: time.Hour})
	d.refreshAgents(t.Context())
	return sock, d
}

func waitItems(t *testing.T, c *client.Client, n int) InboxResult {
	t.Helper()
	var inbox InboxResult
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.Call("inbox", nil, &inbox)
		if len(inbox.Items) >= n {
			return inbox
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wanted %d items, got %+v", n, inbox)
	return inbox
}

// D5/§10: only the person grants respond; a supervisor's answer is verified
// as supervisor:<name>, closes the item, and the asker sees who answered.
func TestSupervisor_AnswersWithinScope(t *testing.T) {
	sock, _ := supervisorSetup(t)
	person := dial(t, sock)
	sup := dialAs(t, sock, "sup")
	w1 := dialAs(t, sock, "w1")
	w3 := dialAs(t, sock, "w3")

	// A pane cannot grant itself; the person can.
	if err := sup.Call("grant", GrantArgs{Principal: "sup", Rights: "respond", Scope: "all"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
	var g store.Grant
	if err := person.Call("grant", GrantArgs{Principal: "sup", Rights: "read,respond", Scope: "session:api"}, &g); err != nil {
		t.Fatal(err)
	}
	if g.GrantedBy != store.Human || g.Parent != 0 || g.Scope != "session:api" {
		t.Fatalf("%+v", g)
	}
	// Before any grant, sup saw nothing; w1 asks, sup sees it, w3's question it does not.
	type res struct {
		r   AskHumanResult
		err error
	}
	got := make(chan res, 1)
	go func() {
		var r AskHumanResult
		err := w1.Call("ask-human", AskHumanArgs{Text: "Merge?", Options: []string{"yes", "no"}, TimeoutMS: 5000}, &r)
		got <- res{r, err}
	}()
	go w3.Call("ask-human", AskHumanArgs{Text: "Deploy web?", TimeoutMS: 5000}, nil)
	waitItems(t, person, 2)
	supView := waitItems(t, sup, 1)
	if len(supView.Items) != 1 || supView.Items[0].From != "w1" {
		t.Fatalf("supervisor sees outside its scope: %+v", supView)
	}
	w1Item := supView.Items[0]
	// Another agent in the session sees it (default read scope) but cannot answer.
	if err := w3.Call("answer", AnswerArgs{Item: w1Item.ID, Text: "yes"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("%v", err)
	}
	if err := sup.Call("answer", AnswerArgs{Item: w1Item.ID, Text: "yes"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err != nil || r.r.Reply.Text != "yes" || r.r.Reply.Verified != "supervisor:sup" || r.r.Reply.From != "sup" {
			t.Fatalf("%+v %v", r.r.Reply, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask-human did not return")
	}
	// Out of scope: w3's item.
	var inbox InboxResult
	person.Call("inbox", nil, &inbox)
	if err := sup.Call("answer", AnswerArgs{Item: inbox.Items[0].ID, Text: "no"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("%v", err)
	}
}

func TestSupervisor_DelegationNarrowerDepthTwoAndRevokeCascades(t *testing.T) {
	sock, _ := supervisorSetup(t)
	person := dial(t, sock)
	sup := dialAs(t, sock, "sup")
	sub := dialAs(t, sock, "sub")
	var g store.Grant
	person.Call("grant", GrantArgs{Principal: "sup", Rights: "read,respond", Scope: "session:api"}, &g)
	// Wider is refused; narrower works; admin never.
	if err := sup.Call("grant", GrantArgs{Principal: "sub", Rights: "respond", Scope: "all"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
	if err := sup.Call("grant", GrantArgs{Principal: "sub", Rights: "respond", Scope: "w3"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("w3 is not in api: %v", err)
	}
	if err := sup.Call("grant", GrantArgs{Principal: "sub", Rights: "admin", Scope: "w1"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
	var sg store.Grant
	if err := sup.Call("grant", GrantArgs{Principal: "sub", Rights: "respond", Scope: "w1"}, &sg); err != nil {
		t.Fatal(err)
	}
	if sg.Parent != g.ID || sg.GrantedBy != "sup" {
		t.Fatalf("%+v", sg)
	}
	// Depth 2: sub cannot delegate further.
	if err := sub.Call("grant", GrantArgs{Principal: "w1", Rights: "respond", Scope: "w1"}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
	var list GrantListResult
	person.Call("grant-list", nil, &list)
	if len(list.Grants) != 2 {
		t.Fatalf("%+v", list)
	}
	// Only the person or the granter revokes; revoking the parent cascades.
	if err := sub.Call("grant-rm", GrantRmArgs{ID: g.ID}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("%v", err)
	}
	var rm map[string]int
	if err := person.Call("grant-rm", GrantRmArgs{ID: g.ID}, &rm); err != nil || rm["removed"] != 2 {
		t.Fatal(rm, err)
	}
	person.Call("grant-list", nil, &list)
	if len(list.Grants) != 0 {
		t.Fatalf("%+v", list)
	}
}

// §8: a request the harness marks risky waits for the person even when a
// supervisor's scope covers it; a --once grant is spent by one answer.
func TestSupervisor_RiskyWaitsForPersonAndOnceIsSpent(t *testing.T) {
	sock, _ := supervisorSetup(t)
	person := dial(t, sock)
	sup := dialAs(t, sock, "sup")
	w1 := dialAs(t, sock, "w1")
	person.Call("grant", GrantArgs{Principal: "sup", Rights: "respond", Scope: "session:api", Once: true}, nil)
	go w1.Call("hold-approval", HoldApprovalArgs{Tool: "Bash", Summary: "git push --force", Risky: true, TimeoutMS: 5000}, nil)
	items := waitItems(t, person, 1)
	if err := sup.Call("answer", AnswerArgs{Item: items.Items[0].ID, Text: "allow"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("risky answered by a supervisor: %v", err)
	}
	var hr map[string]any
	if err := person.Call("answer", AnswerArgs{Item: items.Items[0].ID, Text: "deny"}, &hr); err != nil {
		t.Fatal(err)
	}
	// A plain one the supervisor may decide, once.
	go w1.Call("hold-approval", HoldApprovalArgs{Tool: "Edit", Summary: "x", TimeoutMS: 5000}, nil)
	items = waitItems(t, person, 1)
	var ans map[string]store.Item
	if err := sup.Call("answer", AnswerArgs{Item: items.Items[0].ID, Text: "allow"}, &ans); err != nil {
		t.Fatal(err)
	}
	if ans["item"].DecidedBy != "supervisor:sup" || ans["item"].Answer != "allow" {
		t.Fatalf("%+v", ans["item"])
	}
	var list GrantListResult
	person.Call("grant-list", nil, &list)
	if len(list.Grants) != 0 {
		t.Fatalf("once grant not spent: %+v", list)
	}
	go w1.Call("hold-approval", HoldApprovalArgs{Tool: "Edit", Summary: "y", TimeoutMS: 5000}, nil)
	items = waitItems(t, person, 1)
	if err := sup.Call("answer", AnswerArgs{Item: items.Items[0].ID, Text: "allow"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("spent grant still works: %v", err)
	}
}
