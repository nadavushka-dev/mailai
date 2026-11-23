package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Decided 2026-11-23 for v0 (found on UbCl: every upgrade stopped the page
// and logged the person out). With [web] port the daemon serves the page
// from the start; logins are kept on disk and survive a restart; [web] url
// is where the person reaches it through their own proxy (Tailscale Serve),
// and login links use it.

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitUp(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, err := http.Get(url)
		if err == nil {
			r.Body.Close()
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never came up: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWeb_ServedFromTheStartOnTheConfiguredPort(t *testing.T) {
	port := freePort(t)
	startDaemonWith(t, Options{WebPort: port})
	r := waitUp(t, fmt.Sprintf("http://127.0.0.1:%d/", port))
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session yet: %d", r.StatusCode)
	}
}

func TestWeb_LoginsSurviveARestart(t *testing.T) {
	port := freePort(t)
	file := filepath.Join(t.TempDir(), "web-sessions.json")
	st := store.New(store.Options{})
	sock, stop := startStoppable(t, Options{Store: st, WebPort: port, WebSessions: file})
	var ws WebStartResult
	if err := dial(t, sock).Call("web-start", WebStartArgs{}, &ws); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	if _, err := cl.Get(ws.URL); err != nil {
		t.Fatal(err)
	}
	me := func() int {
		r, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/api/me", port))
		if err != nil {
			return -1
		}
		r.Body.Close()
		return r.StatusCode
	}
	if me() != http.StatusOK {
		t.Fatal("not logged in")
	}
	stop()
	startDaemonWith(t, Options{Store: st, WebPort: port, WebSessions: file})
	waitUp(t, fmt.Sprintf("http://127.0.0.1:%d/", port))
	if code := me(); code != http.StatusOK {
		t.Fatalf("the login did not survive the restart: %d", code)
	}
	// The file holds no usable cookie: only a hash of it.
	if b := readFile(t, file); strings.Contains(b, cookieOf(jar, port)) {
		t.Fatal("the session file holds the cookie itself")
	}
}

// Through a proxy the page's origin is the proxy's: writes from it are the
// page's own; any other origin is still refused.
func TestWeb_PublicURLForLinksAndProxiedWrites(t *testing.T) {
	port := freePort(t)
	sock, _ := startDaemonWith(t, Options{WebPort: port, WebURL: "https://box.tail0.ts.net"})
	var ws WebStartResult
	dial(t, sock).Call("web-start", WebStartArgs{}, &ws)
	if !strings.HasPrefix(ws.URL, "https://box.tail0.ts.net/?t=") || !strings.HasPrefix(ws.Local, fmt.Sprintf("http://127.0.0.1:%d/?t=", port)) {
		t.Fatalf("%+v", ws)
	}
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	cl.Get(ws.Local)
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/mark-read", port), strings.NewReader(`{"thread":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		r, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := post("https://box.tail0.ts.net"); code == http.StatusForbidden {
		t.Fatal("a write from the page at its public URL was refused")
	}
	if code := post("https://evil.tail0.ts.net"); code != http.StatusForbidden {
		t.Fatalf("another origin: %d", code)
	}
}

// mailai web --notify sends a fresh login link to the person's
// notifications, so a phone logs in without a terminal.
func TestWeb_LoginLinkByNotification(t *testing.T) {
	n := &recNotifier{}
	sock, _ := startDaemonWith(t, Options{WebPort: freePort(t), WebURL: "https://box.tail0.ts.net", Notifier: n})
	var ws WebStartResult
	if err := dial(t, sock).Call("web-start", WebStartArgs{Notify: true}, &ws); err != nil {
		t.Fatal(err)
	}
	got := n.wait(t)
	if !strings.Contains(got, ws.URL) {
		t.Fatalf("notified %q, want the link %s", got, ws.URL)
	}
}

// startStoppable is startDaemonWith with a stop the test calls itself.
func startStoppable(t *testing.T, o Options) (string, func()) {
	t.Helper()
	sock := shortSock(t)
	d := New(o)
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return sock, stop
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cookieOf(jar http.CookieJar, port int) string {
	u, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d/", port))
	for _, c := range jar.Cookies(u) {
		if c.Name == "mailai" {
			return c.Value
		}
	}
	return "no-cookie"
}

type recNotifier struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recNotifier) Notify(title, body string) {
	r.mu.Lock()
	r.msgs = append(r.msgs, title+" | "+body)
	r.mu.Unlock()
}

func (r *recNotifier) wait(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.msgs) > 0 {
			m := r.msgs[len(r.msgs)-1]
			r.mu.Unlock()
			return m
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no notification")
	return ""
}
