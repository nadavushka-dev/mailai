package daemon

import (
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
)

// A spoke's ask-human resumes like a local one (SPEC §13a): a timeout names
// the id to come back with, and --wait-for waits on the thread again, or
// returns at once when the answer is already here.

var waitForID = regexp.MustCompile(`come back with --wait-for (\d+)$`)

func resumeID(t *testing.T, err error) uint64 {
	t.Helper()
	if client.Code(err) != proto.CodeTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
	m := waitForID.FindStringSubmatch(err.Error())
	if m == nil {
		t.Fatalf("no --wait-for id in %q", err)
	}
	id, _ := strconv.ParseUint(m[1], 10, 64)
	return id
}

func TestStar_AskHumanOnSpokeResumesWithWaitFor(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	person := dial(t, s.hubSock)
	err := worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}, TimeoutMS: 100}, nil)
	id := resumeID(t, err)
	var inbox InboxResult
	person.Call("inbox", InboxArgs{}, &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].ID != id {
		t.Fatalf("id %d is not the inbox machine's item: %+v", id, inbox.Items)
	}
	if err := worker.Call("ask-human", AskHumanArgs{WaitFor: id, TimeoutMS: 50}, nil); resumeID(t, err) != id {
		t.Fatal("a resumed timeout names another id")
	}
	got := make(chan AskHumanResult, 1)
	go func() {
		var r AskHumanResult
		if err := worker.Call("ask-human", AskHumanArgs{WaitFor: id, TimeoutMS: 5000}, &r); err != nil {
			t.Errorf("resume: %v", err)
		}
		got <- r
	}()
	time.Sleep(20 * time.Millisecond)
	if err := person.Call("answer", AnswerArgs{Item: id, Text: "yes"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.Reply == nil || r.Reply.Text != "yes" || r.Reply.Verified != "human" {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resumed wait never got the answer")
	}
	// Answered already: at once.
	var r AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{WaitFor: id, TimeoutMS: 5000}, &r); err != nil || r.Reply == nil || r.Reply.Text != "yes" {
		t.Fatalf("%+v %v", r, err)
	}
	if err := worker.Call("ask-human", AskHumanArgs{WaitFor: id + 1000, TimeoutMS: 50}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("unknown id: %v", err)
	}
}

// Asked while the link was down: the id is a local handle, and resuming
// after the answer landed returns it at once.
func TestStar_QueuedAskHumanResumes(t *testing.T) {
	s := newStar(t)
	worker := dialAs(t, s.spokeSock, "worker")
	person := dial(t, s.hubSock)
	s.cut(true)
	s.waitLink(t, false)
	var nw AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{Text: "Merge?", NoWait: true}, &nw); err != nil || !nw.Queued || nw.Item.ID == 0 {
		t.Fatalf("%+v %v", nw, err)
	}
	err := worker.Call("ask-human", AskHumanArgs{Text: "Deploy?", Options: []string{"yes", "no"}, TimeoutMS: 100}, nil)
	id := resumeID(t, err)
	if id == nw.Item.ID {
		t.Fatal("two queued asks share a handle")
	}
	s.heal()
	var inbox InboxResult
	waitFor(t, "both questions on the inbox machine", func() bool {
		person.Call("inbox", InboxArgs{}, &inbox)
		return len(inbox.Items) == 2
	})
	for _, it := range inbox.Items {
		if it.Question == "Deploy?" {
			if err := person.Call("answer", AnswerArgs{Item: it.ID, Text: "no"}, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitFor(t, "the answer on the spoke", func() bool {
		var ur UnreadResult
		worker.Call("unread", UnreadArgs{}, &ur)
		return ur.Count == 1
	})
	var r AskHumanResult
	if err := worker.Call("ask-human", AskHumanArgs{WaitFor: id, TimeoutMS: 5000}, &r); err != nil || r.Reply == nil || r.Reply.Text != "no" {
		t.Fatalf("%+v %v", r, err)
	}
	var ur UnreadResult
	worker.Call("unread", UnreadArgs{}, &ur)
	if ur.Count != 0 {
		t.Fatalf("the answer it returned is still unread: %+v", ur)
	}
}
