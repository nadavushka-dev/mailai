package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// The Mail view splits into mail to the person and mail between agents
// (D13): a thread row says who is in it, and how much of it is unread in
// the caller's own mailbox, apart from what its agents have not read.
func TestThreads_PeopleAndUnreadMine(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"}, Placement{Proof: store.ProofToken, Principal: "reviewer"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	var toMe, between SendResult
	worker.Call("send", SendArgs{To: "human", Text: "done"}, &toMe)
	worker.Call("send", SendArgs{To: "reviewer", Text: "please review"}, &between)
	// The person joins the agents' thread: still unread for reviewer.
	person.Call("send", SendArgs{To: "reviewer", Text: "check the cap too", ReplyTo: between.Message.MID}, nil)

	var tr ThreadsResult
	if err := person.Call("threads", nil, &tr); err != nil {
		t.Fatal(err)
	}
	rows := map[string]ThreadRow{}
	for _, r := range tr.Threads {
		rows[r.ThreadID] = r
	}
	me := rows[toMe.Message.MID]
	if fmt.Sprint(me.People) != "[human worker]" || me.UnreadMine != 1 || me.Unread != 1 {
		t.Fatalf("to me: %+v", me)
	}
	ag := rows[between.Message.MID]
	if fmt.Sprint(ag.People) != "[human reviewer worker]" || ag.UnreadMine != 0 || ag.Unread != 2 {
		t.Fatalf("between agents: %+v", ag)
	}
	// For an agent, its own mailbox is what counts as mine.
	rv := dialAs(t, sock, "reviewer")
	rv.Call("threads", nil, &tr)
	if len(tr.Threads) != 1 || tr.Threads[0].UnreadMine != 2 {
		t.Fatalf("reviewer: %+v", tr.Threads)
	}
}

// Opening a thread in the web page marks the person's own mail in it read;
// it never marks an agent's mail, whatever ids it is handed (SPEC §9: only
// the mailbox's owner reads its mail).
func TestWeb_MarkReadOnlyThePersonsOwnMail(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, d := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	var toMe, toAgent SendResult
	worker.Call("send", SendArgs{To: "human", Text: "done"}, &toMe)
	worker.Call("send", SendArgs{To: "reviewer", Text: "please review"}, &toAgent)

	var ws WebStartResult
	if err := person.Call("web-start", WebStartArgs{}, &ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { person.Call("web-stop", nil, nil) })
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	if _, err := cl.Get(ws.URL); err != nil {
		t.Fatal(err)
	}
	base := "http://" + ws.Addr
	if r, _ := cl.Get(base + "/api/mark-read"); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET mark-read: %d", r.StatusCode)
	}
	body := fmt.Sprintf(`{"ids":[%d,%d]}`, toMe.Message.ID, toAgent.Message.ID)
	r, err := cl.Post(base+"/api/mark-read", "application/json", strings.NewReader(body))
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("%v %v", err, r)
	}
	var res ReadResult
	json.NewDecoder(r.Body).Decode(&res)
	r.Body.Close()
	if len(res.Messages) != 1 || res.Messages[0].MID != toMe.Message.MID {
		t.Fatalf("marked %+v", res.Messages)
	}
	if n, _ := d.st.Unread(store.Human); n != 0 {
		t.Fatalf("person still has %d unread", n)
	}
	if n, _ := d.st.Unread("reviewer"); n != 1 {
		t.Fatalf("the agent's mail was marked: %d unread", n)
	}
}

// The page carries the Mail scopes and the join-thread composer.
func TestWeb_MailScopesAndJoinThread(t *testing.T) {
	sock, _ := startDaemon(t, nil)
	person := dial(t, sock)
	var ws WebStartResult
	if err := person.Call("web-start", WebStartArgs{}, &ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { person.Call("web-stop", nil, nil) })
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	r, err := cl.Get(ws.URL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := readAll(r)
	for _, need := range []string{`id="scope-me"`, `id="scope-agents"`, `id="scope-people"`, "Between agents", "Join thread as you", "/api/mark-read", "unread_mine", "people"} {
		if !strings.Contains(page, need) {
			t.Fatalf("page missing %q", need)
		}
	}
}
