package daemon

import (
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §7, D13: ask-human blocks until the person answers from the Inbox;
// the item closes as answered; the answer must be one of the options.
func TestAskHuman_BlocksUntilAnsweredFromInbox(t *testing.T) {
	pl := &fakePlacer{next: []Placement{{Proof: store.ProofToken, Principal: "worker"}}}
	sock, _ := startDaemon(t, pl)
	worker := dial(t, sock)
	person := dial(t, sock)

	type res struct {
		r   AskHumanResult
		err error
	}
	got := make(chan res, 1)
	go func() {
		var r AskHumanResult
		err := worker.Call("ask-human", AskHumanArgs{Text: "Deploy to staging?", Options: []string{"yes", "no"}, TimeoutMS: 5000}, &r)
		got <- res{r, err}
	}()
	var inbox InboxResult
	deadline := time.Now().Add(2 * time.Second)
	for len(inbox.Items) == 0 && time.Now().Before(deadline) {
		person.Call("inbox", nil, &inbox)
		time.Sleep(5 * time.Millisecond)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Kind != store.ItemAskHuman || inbox.Items[0].From != "worker" || inbox.Items[0].Question != "Deploy to staging?" {
		t.Fatalf("%+v", inbox)
	}
	id := inbox.Items[0].ID
	// Wrong option, and an agent trying to answer, are refused.
	if err := person.Call("answer", AnswerArgs{Item: id, Text: "maybe"}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("%v", err)
	}
	if err := worker.Call("answer", AnswerArgs{Item: id, Text: "yes"}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("%v", err)
	}
	if err := worker.Call("dismiss", DismissArgs{Item: id}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("%v", err)
	}
	if err := person.Call("answer", AnswerArgs{Item: id, Text: "yes"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.err != nil || r.r.Reply == nil || r.r.Reply.Text != "yes" || r.r.Reply.Verified != "human" || r.r.Item.ClosedBy != "answered" {
			t.Fatalf("%+v %v", r.r, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask-human did not return")
	}
	person.Call("inbox", nil, &inbox)
	if len(inbox.Items) != 0 {
		t.Fatalf("item still open: %+v", inbox)
	}
	person.Call("inbox", InboxArgs{All: true}, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].ClosedBy != "answered" {
		t.Fatalf("%+v", inbox)
	}
	if err := person.Call("answer", AnswerArgs{Item: id, Text: "yes"}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("answering a closed item: %v", err)
	}
}

func TestAskHuman_NoWaitThenWaitForAndDismiss(t *testing.T) {
	pl := &fakePlacer{next: []Placement{{Proof: store.ProofToken, Principal: "worker"}}}
	sock, _ := startDaemon(t, pl)
	worker := dial(t, sock)
	person := dial(t, sock)
	var r AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Which region?", Options: []string{"us", "eu"}, NoWait: true}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Reply != nil || r.Item.ID == 0 {
		t.Fatalf("%+v", r)
	}
	// Coming back before an answer times out with a hint.
	err := worker.Call("ask-human", AskHumanArgs{WaitFor: r.Item.ID, TimeoutMS: 30}, nil)
	if client.Code(err) != proto.CodeTimeout {
		t.Fatalf("%v", err)
	}
	person.Call("answer", AnswerArgs{Item: r.Item.ID, Text: "eu"}, nil)
	var r2 AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{WaitFor: r.Item.ID}, &r2); err != nil || r2.Reply.Text != "eu" {
		t.Fatalf("%+v %v", r2, err)
	}
	// Another agent cannot wait on it.
	other := dial(t, sock)
	if err := other.Call("ask-human", AskHumanArgs{From: "other", WaitFor: r.Item.ID}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("%v", err)
	}
	// Dismissed: the asker is told it ended without an answer.
	worker.Call("ask-human", AskHumanArgs{Text: "Proceed?", NoWait: true}, &r)
	if err := person.Call("dismiss", DismissArgs{Item: r.Item.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.Call("ask-human", AskHumanArgs{WaitFor: r.Item.ID}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("%v", err)
	}
	// Bounds.
	if err := worker.Call("ask-human", AskHumanArgs{Text: string(make([]byte, MaxQuestion+1)), NoWait: true}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("%v", err)
	}
	if err := person.Call("ask-human", AskHumanArgs{Text: "?", NoWait: true}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("person asking human: %v", err)
	}
}

// A plain ask addressed to human is also an Inbox item, answered the same way.
func TestAskToHumanIsAnItem(t *testing.T) {
	pl := &fakePlacer{next: []Placement{{Proof: store.ProofToken, Principal: "worker"}}}
	sock, _ := startDaemon(t, pl)
	worker := dial(t, sock)
	person := dial(t, sock)
	go worker.Call("ask", AskArgs{To: "human", Text: "free text question", TimeoutMS: 5000}, nil)
	var inbox InboxResult
	deadline := time.Now().Add(2 * time.Second)
	for len(inbox.Items) == 0 && time.Now().Before(deadline) {
		person.Call("inbox", nil, &inbox)
		time.Sleep(5 * time.Millisecond)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Kind != store.ItemAsk {
		t.Fatalf("%+v", inbox)
	}
	sub := dial(t, sock)
	sub.Call("subscribe", nil, nil)
	if err := person.Call("answer", AnswerArgs{Item: inbox.Items[0].ID, Text: "anything goes"}, nil); err != nil {
		t.Fatal(err)
	}
	// Subscribers hear the item close, without the question text.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case f := <-sub.Events():
			if f.Event == "attention" && contains(string(f.Data), `"state":"closed"`) {
				if contains(string(f.Data), "free text") {
					t.Fatal("event leaked the question")
				}
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("no attention event")
}
