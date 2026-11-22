package daemon

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// SPEC §7a: the web page shows an item closed in the session with the same
// badge the TUI uses, next to the maybe badge.
func TestWeb_AnsweredInSessionBadge(t *testing.T) {
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
	for _, need := range []string{`class="badge insession"`, InSessionBadge, `.badge.insession`} {
		if !strings.Contains(body, need) {
			t.Fatalf("page missing %s", need)
		}
	}
}
