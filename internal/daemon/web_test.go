package daemon

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// D11/§12a: the web UI is loopback only, started by the person, entered with
// a one-time token that becomes a cookie; with the cookie, requests act as
// the person; without it, nothing is served.
func TestWeb_TokenCookieAndInboxActions(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
	person := dial(t, sock)
	worker := dialAs(t, sock, "worker")
	if err := worker.Call("web-start", WebStartArgs{}, nil); client.Code(err) != proto.CodeNotHuman {
		t.Fatalf("agent starting the web UI: %v", err)
	}
	var ws WebStartResult
	if err := person.Call("web-start", WebStartArgs{}, &ws); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ws.URL, "http://127.0.0.1:") || !strings.Contains(ws.URL, "/?t=") {
		t.Fatal(ws.URL)
	}
	base := "http://" + ws.Addr
	// No cookie: unauthorized, page and API alike.
	if r, _ := http.Get(base + "/"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("page without session: %d", r.StatusCode)
	}
	if r, _ := http.Get(base + "/api/inbox"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api without session: %d", r.StatusCode)
	}
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	r, err := cl.Get(ws.URL)
	if err != nil || r.StatusCode != http.StatusOK {
		t.Fatalf("%v %d", err, r.StatusCode)
	}
	body, _ := readAll(r)
	if !strings.Contains(body, "<title>mailai</title>") {
		t.Fatalf("page: %.80s", body)
	}
	// The page is the Inbox, not a prompt() dialog: a composer and the
	// mail filters the TUI has are in the document itself.
	if strings.Contains(body, "prompt(") {
		t.Fatal("page uses prompt()")
	}
	for _, need := range []string{`id="composer"`, `id="f-who"`, `id="f-session"`, `id="f-machine"`, `id="f-unread"`} {
		if !strings.Contains(body, need) {
			t.Fatalf("page missing %s", need)
		}
	}
	// The token is one-time.
	fresh := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r2, _ := fresh.Get(ws.URL)
	if len(r2.Cookies()) != 0 {
		t.Fatal("token reused")
	}
	// An agent asks; the Inbox shows it; the web answers as the person.
	go worker.Call("ask-human", AskHumanArgs{Text: "Ship it?", Options: []string{"yes", "no"}, TimeoutMS: 5000}, nil)
	var inbox InboxResult
	deadline := time.Now().Add(2 * time.Second)
	for len(inbox.Items) == 0 && time.Now().Before(deadline) {
		r, _ := cl.Get(base + "/api/inbox")
		json.NewDecoder(r.Body).Decode(&inbox)
		r.Body.Close()
		time.Sleep(10 * time.Millisecond)
	}
	if len(inbox.Items) != 1 || inbox.Items[0].Question != "Ship it?" {
		t.Fatalf("%+v", inbox)
	}
	// SSE: subscribe, then act, then expect an attention event.
	events := make(chan string, 8)
	go func() {
		r, err := cl.Get(base + "/api/events")
		if err != nil {
			return
		}
		sc := bufio.NewScanner(r.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event: ") {
				events <- strings.TrimPrefix(sc.Text(), "event: ")
			}
		}
	}()
	select {
	case ev := <-events:
		if ev != "hello" {
			t.Fatal(ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE hello")
	}
	r, _ = cl.Post(base+"/api/answer", "application/json", strings.NewReader(`{"item":1,"text":"maybe"}`))
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad option: %d", r.StatusCode)
	}
	r, _ = cl.Post(base+"/api/answer", "application/json", strings.NewReader(`{"item":1,"text":"yes"}`))
	if r.StatusCode != http.StatusOK {
		b, _ := readAll(r)
		t.Fatalf("answer: %d %s", r.StatusCode, b)
	}
	deadline = time.Now().Add(2 * time.Second)
	seen := false
	for !seen && time.Now().Before(deadline) {
		select {
		case ev := <-events:
			seen = ev == "attention" || ev == "mail"
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !seen {
		t.Fatal("no event after answering")
	}
	// The reply is the person's, verified.
	var rr ReadResult
	worker.Call("read", ReadArgs{}, &rr)
	if len(rr.Messages) != 1 || rr.Messages[0].Verified != "human" || rr.Messages[0].Text != "yes" {
		t.Fatalf("%+v", rr.Messages)
	}
	// Threads and thread via the API; reply via the API is the person's too.
	r, _ = cl.Get(base + "/api/threads")
	var tr ThreadsResult
	json.NewDecoder(r.Body).Decode(&tr)
	if len(tr.Threads) != 1 {
		t.Fatalf("%+v", tr)
	}
	r, _ = cl.Post(base+"/api/reply", "application/json", strings.NewReader(`{"reply_to":"`+tr.Threads[0].ThreadID+`","to":"worker","text":"and thanks","from":"worker"}`))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("reply: %d", r.StatusCode)
	}
	worker.Call("read", ReadArgs{}, &rr)
	last := rr.Messages[len(rr.Messages)-1]
	if last.From != store.Human || last.Verified != "human" {
		t.Fatalf("web reply must be the person's whatever the body claims: %+v", last)
	}
	// Revoke ends the session; stop ends the server.
	person.Call("web-revoke", nil, nil)
	if r, _ := cl.Get(base + "/api/inbox"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after revoke: %d", r.StatusCode)
	}
	person.Call("web-stop", nil, nil)
	if _, err := fresh.Get(base + "/"); err == nil {
		t.Fatal("server still up after stop")
	}
}

func readAll(r *http.Response) (string, error) {
	defer r.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
	}
	return b.String(), sc.Err()
}
