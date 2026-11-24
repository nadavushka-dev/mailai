package daemon

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Every delete leaves one line in the daemon log (decided 2026-11-24, after
// a bulk delete on UbCl left no trace): who (the person and through what,
// or expiry), how many messages in how many threads, and the thread ids,
// the first five then a count. The person's delete hides (option A).
func TestDelete_LeavesOneLogLine(t *testing.T) {
	logs := &syncBuf{}
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, _ := startDaemonWith(t, Options{Placer: pl, Log: log.New(logs, "", 0)})
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	reviewer := dialAs(t, sock, "reviewer")
	send := func(text, replyTo string) SendResult {
		t.Helper()
		var r SendResult
		if err := worker.Call("send", SendArgs{To: "reviewer", Text: text, ReplyTo: replyTo}, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	lines := func() []string {
		var out []string
		for _, l := range strings.Split(logs.String(), "\n") {
			if strings.HasPrefix(l, "hid ") {
				out = append(out, l)
			}
		}
		return out
	}
	last := func() string { l := lines(); return l[len(l)-1] }

	root := send("a", "")
	send("b", root.Message.MID)
	if err := person.Call("delete", DeleteArgs{Thread: root.Message.ThreadID, Via: "cli"}, nil); err != nil {
		t.Fatal(err)
	}
	if want := "hid 2 messages in 1 thread from the person by human via cli: " + root.Message.ThreadID; last() != want {
		t.Fatalf("log %q, want %q", last(), want)
	}
	one := send("c", "")
	if err := person.Call("delete", DeleteArgs{MIDs: []string{one.Message.MID}, Via: "tui"}, nil); err != nil {
		t.Fatal(err)
	}
	if want := "hid 1 message in 1 thread from the person by human via tui: " + one.Message.ThreadID; last() != want {
		t.Fatalf("log %q, want %q", last(), want)
	}
	// A client that does not say what it is: the socket. The web says so
	// itself, whatever the body claims.
	two := send("d", "")
	if err := person.Call("delete", DeleteArgs{Thread: two.Message.ThreadID}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(last(), "by human via socket: "+two.Message.ThreadID) {
		t.Fatalf("log %q", last())
	}
	three := send("e", "")
	cl, base := webClient(t, person)
	r, err := cl.Post(base+"/api/delete", "application/json", strings.NewReader(`{"thread":"`+three.Message.ThreadID+`","via":"cli"}`))
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("%v %v", err, r)
	}
	r.Body.Close()
	if want := "hid 1 message in 1 thread from the person by human via web: " + three.Message.ThreadID; last() != want {
		t.Fatalf("log %q, want %q", last(), want)
	}
	// In one go: the first five thread ids, then how many more.
	var ids []string
	for i := 0; i < 7; i++ {
		// From the reviewer: the worker's burst is spent (rate limit).
		var r SendResult
		if err := reviewer.Call("send", SendArgs{To: "worker", Text: fmt.Sprint("bulk ", i)}, &r); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.Message.ThreadID)
	}
	for _, c := range []store.Address{"worker", "reviewer"} {
		if err := dialAs(t, sock, c).Call("read", ReadArgs{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	n := len(lines())
	if err := person.Call("delete", DeleteArgs{ReadBetweenAgents: true, DryRun: true, Via: "cli"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(lines()) != n {
		t.Fatalf("a dry run logged %q", last())
	}
	if err := person.Call("delete", DeleteArgs{ReadBetweenAgents: true, Via: "cli"}, nil); err != nil {
		t.Fatal(err)
	}
	got := last()
	if !strings.HasPrefix(got, "hid 7 messages in 7 threads from the person by human via cli (read between agents): ") || !strings.HasSuffix(got, " +2 more") {
		t.Fatalf("log %q", got)
	}
	shown := 0
	for _, id := range ids {
		if strings.Contains(got, " "+id+" ") || strings.Contains(got, ": "+id+" ") {
			shown++
		}
	}
	if shown != 5 || len(lines()) != n+1 {
		t.Fatalf("%d ids shown in %q; %d lines", shown, got, len(lines()))
	}
}

// Expiry says so too, with the threads it took mail from.
func TestExpire_LeavesOneLogLine(t *testing.T) {
	logs := &syncBuf{}
	c := &testClock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	st := store.New(store.Options{Now: c.now})
	old, _ := st.Send(store.SendRequest{From: "a", To: "worker", Text: "read long ago"})
	st.Read(store.ReadQuery{Mailbox: "worker", IDs: []uint64{old.ID}, Mark: true})
	c.add(8 * 24 * time.Hour)
	sock, _ := startDaemonWith(t, Options{Store: st, Now: c.now, KeepFor: 7 * 24 * time.Hour, ExpireEvery: time.Hour, Log: log.New(logs, "", 0)})
	dial(t, sock).Call("ping", nil, nil)
	want := "deleted 1 message in 1 thread by expiry (read over 168h0m0s ago): " + old.ThreadID
	if !strings.Contains(logs.String(), want+"\n") {
		t.Fatalf("log:\n%s\nwant %q", logs.String(), want)
	}
}
