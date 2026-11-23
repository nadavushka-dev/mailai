package daemon

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// webClient starts the web UI as the person and returns a logged-in client
// and the base URL.
func webClient(t *testing.T, person interface {
	Call(string, any, any) error
}) (*http.Client, string) {
	t.Helper()
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
	return cl, "http://" + ws.Addr
}

func get(t *testing.T, cl *http.Client, url string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	r, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return r, string(b)
}

// An ask-human can carry files for the person to look at (SPEC §6): they are
// references, and the Inbox item shows them.
func TestAskHuman_AttachmentsReachTheInboxItem(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "builder"})
	sock, _ := startDaemon(t, pl)
	shot := filepath.Join(t.TempDir(), "design.png")
	os.WriteFile(shot, []byte("\x89PNG\r\n\x1a\nfake"), 0o644)
	builder := dialAs(t, sock, "builder")
	var res AskHumanResult
	err := builder.Call("ask-human", AskHumanArgs{Text: "Ship this design?", Options: []string{"ship", "redo"}, NoWait: true,
		Attachments: []store.Attachment{{Path: shot}}}, &res)
	if err != nil {
		t.Fatal(err)
	}
	var in InboxResult
	dial(t, sock).Call("inbox", InboxArgs{}, &in)
	if len(in.Items) != 1 || len(in.Items[0].Attachments) != 1 {
		t.Fatalf("inbox: %+v", in.Items)
	}
	a := in.Items[0].Attachments[0]
	if a.Path != shot || a.Kind != "image" || a.Bytes != 12 {
		t.Fatalf("attachment: %+v", a)
	}
	// A bad path is refused before anything is asked.
	err = builder.Call("ask-human", AskHumanArgs{Text: "and this?", NoWait: true, Attachments: []store.Attachment{{Path: "rel.png"}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative path: %v", err)
	}
}

// The web page shows an attachment by reading the file where it lives, at
// the time it is opened: never a copy, ranges for big files, and the
// message says when the file changed or went away since it was sent.
func TestWeb_ArtifactIsServedByReference(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "builder"})
	sock, _ := startDaemon(t, pl)
	dir := t.TempDir()
	report := filepath.Join(dir, "report.txt")
	os.WriteFile(report, []byte("first version"), 0o644)
	var sent SendResult
	if err := dialAs(t, sock, "builder").Call("send", SendArgs{To: "human", Text: "see attached", Attachments: []store.Attachment{{Path: report}}}, &sent); err != nil {
		t.Fatal(err)
	}
	cl, base := webClient(t, dial(t, sock))
	url := base + "/api/artifact?mid=" + sent.Message.MID + "&i=0"

	r, body := get(t, cl, url)
	if r.StatusCode != 200 || body != "first version" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("artifact: %d %q %q", r.StatusCode, body, r.Header.Get("Content-Type"))
	}
	if r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("nosniff missing: %v", r.Header)
	}
	if r, body = get(t, cl, url, "Range", "bytes=0-4"); r.StatusCode != http.StatusPartialContent || body != "first" {
		t.Fatalf("range: %d %q", r.StatusCode, body)
	}
	if r, _ = get(t, cl, url+"&dl=1"); !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %q", r.Header.Get("Content-Disposition"))
	}

	// Changed after it was sent: today's content, and the thread says so.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(report, []byte("second, longer version"), 0o644)
	if _, body = get(t, cl, url); body != "second, longer version" {
		t.Fatalf("reference: %q", body)
	}
	var th struct{ Messages []store.Message }
	_, js := get(t, cl, base+"/api/thread?id="+sent.Message.MID)
	if !strings.Contains(js, `"changed":true`) {
		t.Fatalf("thread does not say changed: %s", js)
	}
	_ = th

	// Gone: 404, and missing in the thread.
	os.Remove(report)
	if r, _ = get(t, cl, url); r.StatusCode != http.StatusNotFound {
		t.Fatalf("gone: %d", r.StatusCode)
	}
	if _, js = get(t, cl, base+"/api/thread?id="+sent.Message.MID); !strings.Contains(js, `"missing":true`) {
		t.Fatalf("thread does not say missing: %s", js)
	}
	// Replaced by a directory: not a file, not served.
	os.Mkdir(report, 0o755)
	if r, _ = get(t, cl, url); r.StatusCode != http.StatusNotFound {
		t.Fatalf("directory: %d", r.StatusCode)
	}
	// Only what a message attached, and only with a session.
	for _, u := range []string{base + "/api/artifact?mid=" + sent.Message.MID + "&i=1", base + "/api/artifact?mid=nope:1&i=0", base + "/api/artifact?mid=" + sent.Message.MID + "&i=x"} {
		if r, _ = get(t, cl, u); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d", u, r.StatusCode)
		}
	}
	if r, _ := http.Get(url); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session: %d", r.StatusCode)
	}
}

// An agent's HTML (or SVG, or anything a browser might run) opens in a
// sandbox: its scripts run, but in an opaque origin with no way to reach
// the page's session. Pictures and PDFs open as themselves.
func TestWeb_ArtifactThatCouldRunIsSandboxed(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "builder"})
	sock, _ := startDaemon(t, pl)
	dir := t.TempDir()
	files := map[string]string{
		"report.html": "<script>fetch('/api/inbox')</script>",
		"chart.svg":   "<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>",
		"notes.md":    "# notes",
		"shot.png":    "\x89PNG\r\n\x1a\nfake",
		"spec.pdf":    "%PDF-1.4 fake",
	}
	var atts []store.Attachment
	names := []string{"report.html", "chart.svg", "notes.md", "shot.png", "spec.pdf"}
	for _, n := range names {
		p := filepath.Join(dir, n)
		os.WriteFile(p, []byte(files[n]), 0o644)
		atts = append(atts, store.Attachment{Path: p})
	}
	var sent SendResult
	if err := dialAs(t, sock, "builder").Call("send", SendArgs{To: "human", Text: "files", Attachments: atts}, &sent); err != nil {
		t.Fatal(err)
	}
	cl, base := webClient(t, dial(t, sock))
	for i, n := range names {
		r, _ := get(t, cl, base+"/api/artifact?mid="+sent.Message.MID+"&i="+string(rune('0'+i)))
		csp := r.Header.Get("Content-Security-Policy")
		ct := r.Header.Get("Content-Type")
		switch n {
		case "shot.png", "spec.pdf":
			if strings.Contains(csp, "sandbox") {
				t.Fatalf("%s should open as itself: %q", n, csp)
			}
		default:
			if !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") {
				t.Fatalf("%s not sandboxed: %q", n, csp)
			}
		}
		if n == "notes.md" && !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("markdown should read as text: %q", ct)
		}
	}
}

// A thread row says how many files its messages attach, for the paperclip
// in the Mail list.
func TestThreads_CountFiles(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "builder"})
	sock, _ := startDaemon(t, pl)
	p := filepath.Join(t.TempDir(), "a.log")
	os.WriteFile(p, []byte("log"), 0o644)
	b := dialAs(t, sock, "builder")
	var first SendResult
	b.Call("send", SendArgs{To: "human", Text: "logs", Attachments: []store.Attachment{{Path: p}, {Path: p}}}, &first)
	b.Call("send", SendArgs{To: "human", Text: "one more", ReplyTo: first.Message.MID, Attachments: []store.Attachment{{Path: p}}}, nil)
	b.Call("send", SendArgs{To: "human", Text: "no files"}, nil)
	var tr ThreadsResult
	dial(t, sock).Call("threads", nil, &tr)
	files := map[string]int{}
	for _, r := range tr.Threads {
		files[r.First] = r.Files
	}
	if files["logs"] != 3 || files["no files"] != 0 {
		t.Fatalf("files: %v", files)
	}
}

// Writes come from the page itself. Cookies do not care about ports, so a
// page an agent serves on another localhost port (or an attachment, in its
// null origin) could otherwise POST with the person's session: a write must
// be JSON (cross-origin JSON needs a CORS preflight this server never
// grants) and, when the browser says where it comes from, from this origin.
func TestWeb_WritesOnlyFromThePageItself(t *testing.T) {
	pl := placed(Placement{Proof: store.ProofToken, Principal: "builder"})
	sock, _ := startDaemon(t, pl)
	var res AskHumanResult
	dialAs(t, sock, "builder").Call("ask-human", AskHumanArgs{Text: "ok?", NoWait: true}, &res)
	cl, base := webClient(t, dial(t, sock))
	body := `{"item":` + strconv.FormatUint(res.Item.ID, 10) + `}`
	post := func(hdr ...string) int {
		req, _ := http.NewRequest("POST", base+"/api/dismiss", strings.NewReader(body))
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		r, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	for _, bad := range [][]string{
		{"Content-Type", "text/plain"},
		{},
		{"Content-Type", "application/json", "Origin", "http://127.0.0.1:9999"},
		{"Content-Type", "application/json", "Origin", "null"},
		{"Content-Type", "application/json", "Sec-Fetch-Site", "same-site"},
	} {
		if code := post(bad...); code != http.StatusForbidden && code != http.StatusUnsupportedMediaType {
			t.Fatalf("%v: %d", bad, code)
		}
	}
	var in InboxResult
	dial(t, sock).Call("inbox", InboxArgs{}, &in)
	if len(in.Items) != 1 {
		t.Fatalf("a forged write went through: %+v", in.Items)
	}
	if code := post("Content-Type", "application/json; charset=utf-8", "Origin", base, "Sec-Fetch-Site", "same-origin"); code != http.StatusOK {
		t.Fatalf("the page's own write: %d", code)
	}
}

// The page knows how to show artifacts.
// Decided 2026-11-23: the page raises a desktop notification for a new
// Inbox item while it is in the background, behind a bell toggle that is
// off until the person turns it on, kept per browser.
func TestWeb_PageNotifiesWhenAskedTo(t *testing.T) {
	pl := placed()
	sock, _ := startDaemon(t, pl)
	cl, base := webClient(t, dial(t, sock))
	_, page := get(t, cl, base+"/")
	for _, want := range []string{`id="notify-btn"`, `id="i-bell"`, "new Notification(", "requestPermission", "'mailai.notify'", "document.hidden",
		// Turning it on shows one at once, so a blocked chain shows itself.
		"Notifications are on"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
}

// Parity with the TUI (§12a): G to the last row, gg to the first.
func TestWeb_PageJumpsWithGAndGG(t *testing.T) {
	pl := placed()
	sock, _ := startDaemon(t, pl)
	cl, base := webClient(t, dial(t, sock))
	_, page := get(t, cl, base+"/")
	for _, want := range []string{"e.key === 'G'", "pendingG"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
}

func TestWeb_PageShowsArtifacts(t *testing.T) {
	pl := placed()
	sock, _ := startDaemon(t, pl)
	cl, base := webClient(t, dial(t, sock))
	_, page := get(t, cl, base+"/")
	for _, want := range []string{"/api/artifact", "attachments", "changed since sent"} {
		if !strings.Contains(page, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
}
