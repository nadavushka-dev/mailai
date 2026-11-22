package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §7a, §12a parity: the web Inbox has the Recently closed strip, the
// maybe badge, and the thread's one-line note, fed by the same verbs.
func TestWeb_InSessionStripBadgeAndThreadNote(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "worker"})
	sock, _ := startDaemon(t, pl)
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
	body, _ := readAll(r)
	for _, need := range []string{`id="recent"`, `id="recent-list"`, `maybe answered in session`, `class="sysnote"`, `answered in the session at`} {
		if !strings.Contains(body, need) {
			t.Fatalf("page missing %s", need)
		}
	}
	base := "http://" + ws.Addr
	get := func(path string, v any) {
		t.Helper()
		r, err := cl.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	// One ask closed in the session, one marked maybe.
	gone := rawDialAs(t, sock, "worker")
	go gone.Call("ask-human", AskHumanArgs{Text: "Merge?"}, nil)
	items := waitItems(t, person, 1)
	closedID := items.Items[0].ID
	gone.Close()
	closed := waitClosed(t, person, closedID)
	worker := dialAs(t, sock, "worker")
	var open AskHumanResult
	worker.Call("ask-human", AskHumanArgs{Text: "Region?", NoWait: true}, &open)
	worker.Call("report-state", ReportStateArgs{State: "working", Signal: SignalPrompt}, nil)

	var inbox InboxResult
	get("/api/inbox", &inbox)
	if len(inbox.Items) != 1 || inbox.Items[0].MaybeInSessionAt == 0 {
		t.Fatalf("%+v", inbox.Items)
	}
	var recent InboxResult
	get("/api/inbox?closed=1", &recent)
	if len(recent.Items) != 1 || recent.Items[0].ClosedBy != store.ClosedInSession {
		t.Fatalf("%+v", recent.Items)
	}
	var th struct {
		Messages []store.Message `json:"messages"`
		Items    []store.Item    `json:"items"`
	}
	get("/api/thread?id="+closed.ThreadID, &th)
	if len(th.Messages) != 1 || len(th.Items) != 1 || th.Items[0].ClosedBy != store.ClosedInSession {
		t.Fatalf("%+v", th)
	}
}
