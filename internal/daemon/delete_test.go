package daemon

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Decided 2026-11-24 (SPEC §6): mail would clutter the Mail view, so the
// person deletes by hand (a thread or a message) and read mail goes by
// itself after [mail] keep_days. Agents cannot delete.

func TestDelete_PersonDeletesAThreadAgentsCannot(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
	var root, other SendResult
	worker.Call("send", SendArgs{To: "reviewer", Text: "please review"}, &root)
	reviewer.Call("send", SendArgs{To: "worker", Text: "looks fine", ReplyTo: root.Message.MID}, nil)
	worker.Call("send", SendArgs{To: "reviewer", Text: "another thing"}, &other)

	err := worker.Call("delete", DeleteArgs{Thread: root.Message.ThreadID}, nil)
	if client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("an agent deleting: %v", err)
	}
	if len(d.st.Thread(root.Message.ThreadID)) != 2 {
		t.Fatal("an agent's delete went through")
	}

	var res DeleteResult
	if err := person.Call("delete", DeleteArgs{Thread: root.Message.ThreadID}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 2 {
		t.Fatalf("deleted %d, want 2", res.Deleted)
	}
	var tr ThreadsResult
	person.Call("threads", nil, &tr)
	if len(tr.Threads) != 1 || tr.Threads[0].ThreadID != other.Message.ThreadID {
		t.Fatalf("threads left: %+v", tr.Threads)
	}
	// Nothing to delete is an error the person can see, not a silent 0.
	if err := person.Call("delete", DeleteArgs{Thread: root.Message.ThreadID}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("deleting it again: %v", err)
	}
	if err := person.Call("delete", DeleteArgs{}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("deleting nothing: %v", err)
	}
}

func TestDelete_OneMessage(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	var root, reply SendResult
	worker.Call("send", SendArgs{To: "human", Text: "done"}, &root)
	worker.Call("send", SendArgs{To: "human", Text: "and a typo", ReplyTo: root.Message.MID}, &reply)
	var res DeleteResult
	if err := person.Call("delete", DeleteArgs{MIDs: []string{reply.Message.MID}}, &res); err != nil || res.Deleted != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	// Hidden from the person only (option A, 2026-11-24): the worker who
	// sent it still has the whole thread.
	if th := readThread(t, person, root.Message.ThreadID); len(th) != 1 || th[0].MID != root.Message.MID {
		t.Fatalf("person's thread now %+v", th)
	}
	if th := readThread(t, worker, root.Message.ThreadID); len(th) != 2 || len(d.st.Thread(root.Message.ThreadID)) != 2 {
		t.Fatalf("the worker's thread now %+v", th)
	}
}

// The web Mail view deletes a thread, and its subscribers hear of it.
func TestWeb_DeleteThread(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	var root SendResult
	dialAs(t, sock, "worker").Call("send", SendArgs{To: "human", Text: "done"}, &root)
	if err := person.Call("subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	cl, base := webClient(t, person)
	_, page := get(t, cl, base+"/")
	for _, want := range []string{`data-a="delete-thread"`, "/api/delete", "confirm(", "'deleted'"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	if r, _ := cl.Get(base + "/api/delete"); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET delete: %d", r.StatusCode)
	}
	r, err := cl.Post(base+"/api/delete", "application/json", strings.NewReader(`{"thread":"`+root.Message.ThreadID+`"}`))
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("%v %v", err, r)
	}
	r.Body.Close()
	// Hidden from the person; the worker keeps it (option A, 2026-11-24).
	if personSees(t, person, root.Message.ThreadID) {
		t.Fatal("the person still sees the thread")
	}
	if len(d.st.Thread(root.Message.ThreadID)) != 1 {
		t.Fatal("the worker's mail went")
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-person.Events():
			if ev.Event == "deleted" {
				return
			}
		case <-deadline:
			t.Fatal("no deleted event")
		}
	}
}

// Read mail older than KeepFor goes at start and on every sweep; unread
// mail stays however old.
func TestExpire_SweepsAtStartAndThenOnTheTick(t *testing.T) {
	c := &testClock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	st := store.New(store.Options{Now: c.now})
	old, _ := st.Send(store.SendRequest{From: "a", To: "worker", Text: "read long ago"})
	unread, _ := st.Send(store.SendRequest{From: "a", To: "worker", Text: "never read"})
	st.Read(store.ReadQuery{Mailbox: "worker", IDs: []uint64{old.ID}, Mark: true})
	c.add(8 * 24 * time.Hour)
	later, _ := st.Send(store.SendRequest{From: "a", To: "worker", Text: "read now"})
	st.Read(store.ReadQuery{Mailbox: "worker", IDs: []uint64{later.ID}, Mark: true})

	sock, _ := startDaemonWith(t, Options{Store: st, Now: c.now, KeepFor: 7 * 24 * time.Hour, ExpireEvery: 20 * time.Millisecond})
	dial(t, sock).Call("ping", nil, nil)
	if _, ok := st.Get(old.MID); ok {
		t.Fatal("old read mail survived the start sweep")
	}
	c.add(8 * 24 * time.Hour)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := st.Get(later.MID); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tick never swept")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := st.Get(unread.MID); !ok {
		t.Fatal("unread mail was swept")
	}
}
