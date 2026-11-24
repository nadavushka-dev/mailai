package daemon

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// SPEC §12: mail between agents carries no new or unread marks for the
// person; only mail to or from the person counts as new. The page keeps
// the person's own unread dot and counts, and the read receipts inside a
// thread.
func TestWeb_MailBetweenAgentsIsQuiet(t *testing.T) {
	sock, _ := startDaemon(t, placed())
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
	for _, gone := range []string{`id="n-agents"`, "new since you last looked", `class="tag new"`, ".tag.new", "not read yet by the agent", "SEEN_KEY", "agentsMark", "agentsSeenAt"} {
		if strings.Contains(page, gone) {
			t.Fatalf("page still has %s", gone)
		}
	}
	for _, kept := range []string{`class="unread-dot"`, `id="n-me"`, `id="mail-count"`, "not read yet</span>", "read ${hhmm(readAt)}"} {
		if !strings.Contains(page, kept) {
			t.Fatalf("page lost %s", kept)
		}
	}
}

// SPEC §12: "Unread only" keeps threads with the person's own unread mail,
// not threads where only an agent has some.
func TestWeb_UnreadFilterIsThePersons(t *testing.T) {
	sock, _ := startDaemon(t, placed())
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
	if !strings.Contains(page, "if (f.unread && !t.unread_mine) return false;") || strings.Contains(page, "f.unread && !t.unread)") {
		t.Fatal("the unread filter must count only the person's unread (unread_mine)")
	}
}
