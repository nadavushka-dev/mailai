package daemon

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// webPage logs a person into a fresh daemon's web page and returns it.
func webPage(t *testing.T) string {
	t.Helper()
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
	return page
}

// todo #5: a row in the Recently closed strip opens its thread when the
// thread is held here; otherwise it is plain text, no error.
func TestWeb_RecentlyClosedRowOpensItsThread(t *testing.T) {
	page := webPage(t)
	for _, need := range []string{
		"const held = it.thread_id && threads.some(t => t.thread_id === it.thread_id);",
		`class="recent-row opens" data-a="open" data-id="${esc(it.thread_id)}"`,
		".recent-row.opens { cursor: pointer; }",
	} {
		if !strings.Contains(page, need) {
			t.Fatalf("page missing %s", need)
		}
	}
}
