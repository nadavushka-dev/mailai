package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// The web UI (D11, SPEC §12a): the Inbox in a browser, served by the daemon
// on loopback only, off until the person asks. The one-time token in the
// URL becomes a session cookie; a request with a valid cookie acts as the
// person, which is the nonce model of SPEC §9 for a client with no pid. It
// shows what inbox, threads and read return, and nothing else.

//go:embed webui/index.html
var webFS embed.FS

const webSessionTTL = 12 * time.Hour

type webState struct {
	mu       sync.Mutex
	srv      *http.Server
	ln       net.Listener
	tokens   map[string]time.Time // one-time, unused
	sessions map[string]time.Time // sha256(cookie) -> expiry

	port int           // [web] port: served from the start
	url  string        // [web] url: the person's way in (a proxy), no trailing /
	file string        // sessions kept here across restarts
	ttl  time.Duration // how long a login lasts
}

type WebStartArgs struct {
	Port int `json:"port,omitempty"`
	// Notify also sends the login link to the person's notifications, so a
	// phone logs in without a terminal.
	Notify bool `json:"notify,omitempty"`
}

type WebStartResult struct {
	// URL is the login link: at [web] url when set, else on loopback.
	URL  string `json:"url"`
	Addr string `json:"addr"`
	// Local is the same link on loopback, when URL is the public one.
	Local string `json:"local,omitempty"`
}

// webPerson is the placement a web request acts with.
var webPerson = Placement{Proof: store.ProofOutsidePane}

func (d *Daemon) webVerb(c *conn, verb string, raw []byte) (any, error) {
	if !isPerson(c.pl) {
		return nil, proto.Errorf(proto.CodeNotHuman, "only the person, from outside every pane, starts or stops the web UI")
	}
	switch verb {
	case "web-start":
		a, err := decode[WebStartArgs](raw)
		if err != nil {
			return nil, err
		}
		res, err := d.webStart(a.Port)
		if err == nil && a.Notify {
			if d.notifier == nil {
				return nil, proto.Errorf(proto.CodeBadRequest, "notifications are off ([notify] in config.toml)")
			}
			d.notifier.Notify("mailai login", res.URL)
		}
		return res, err
	case "web-stop":
		return d.webStop()
	case "web-revoke":
		d.web.mu.Lock()
		n := len(d.web.sessions) + len(d.web.tokens)
		d.web.sessions, d.web.tokens = map[string]time.Time{}, map[string]time.Time{}
		d.saveWebSessionsLocked()
		d.web.mu.Unlock()
		return map[string]int{"revoked": n}, nil
	}
	return nil, proto.Errorf(proto.CodeUnknownVerb, "%q", verb)
}

// webStart serves the page if it is not served yet and mints a one-time
// login link.
func (d *Daemon) webStart(port int) (WebStartResult, error) {
	if port == 0 {
		port = d.web.port
	}
	if err := d.webServe(port); err != nil {
		return WebStartResult{}, err
	}
	d.web.mu.Lock()
	defer d.web.mu.Unlock()
	tok := randHex(16)
	d.web.tokens[tok] = d.now().Add(10 * time.Minute)
	addr := d.web.ln.Addr().String()
	local := fmt.Sprintf("http://%s/?t=%s", addr, tok)
	if d.web.url != "" {
		return WebStartResult{URL: d.web.url + "/?t=" + tok, Addr: addr, Local: local}, nil
	}
	return WebStartResult{URL: local, Addr: addr}, nil
}

// webServe starts the HTTP server on 127.0.0.1:port unless it runs.
func (d *Daemon) webServe(port int) error {
	d.web.mu.Lock()
	defer d.web.mu.Unlock()
	if d.web.srv == nil {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return proto.Errorf(proto.CodeInternal, "listen: %v", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/", d.webIndex)
		mux.HandleFunc("/api/me", d.webAuth(d.apiMe))
		mux.HandleFunc("/api/inbox", d.webAuth(d.apiInbox))
		mux.HandleFunc("/api/threads", d.webAuth(d.apiThreads))
		mux.HandleFunc("/api/thread", d.webAuth(d.apiThread))
		mux.HandleFunc("/api/agents", d.webAuth(d.apiAgents))
		mux.HandleFunc("/api/answer", d.webAuth(d.apiAnswer))
		mux.HandleFunc("/api/dismiss", d.webAuth(d.apiDismiss))
		mux.HandleFunc("/api/delete", d.webAuth(d.apiDelete))
		mux.HandleFunc("/api/reply", d.webAuth(d.apiReply))
		mux.HandleFunc("/api/mark-read", d.webAuth(d.apiMarkRead))
		mux.HandleFunc("/api/artifact", d.webAuth(d.apiArtifact))
		mux.HandleFunc("/api/events", d.webAuth(d.apiEvents))
		d.web.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		d.web.ln = ln
		if d.web.tokens == nil {
			d.web.tokens = map[string]time.Time{}
		}
		if d.web.sessions == nil {
			d.web.sessions = map[string]time.Time{}
		}
		go d.web.srv.Serve(ln)
	}
	return nil
}

// webStop is mailai web --stop: the page goes and so do its logins.
func (d *Daemon) webStop() (any, error) {
	d.web.mu.Lock()
	d.web.sessions, d.web.tokens = map[string]time.Time{}, map[string]time.Time{}
	d.saveWebSessionsLocked()
	d.web.mu.Unlock()
	return map[string]bool{"stopped": d.webShutdown()}, nil
}

// webShutdown stops serving and keeps the logins, for the next daemon.
func (d *Daemon) webShutdown() bool {
	d.web.mu.Lock()
	srv := d.web.srv
	d.web.srv, d.web.ln = nil, nil
	d.web.mu.Unlock()
	if srv == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	return true
}

// sessionKey is what is kept of a login cookie: its hash, so the file
// holds nothing a browser could present.
func sessionKey(cookie string) string {
	h := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(h[:])
}

// loadWebSessions reads the logins a previous daemon kept, dropping
// expired ones.
func (d *Daemon) loadWebSessions() {
	d.web.sessions = map[string]time.Time{}
	if d.web.file == "" {
		return
	}
	b, err := os.ReadFile(d.web.file)
	if err != nil {
		return
	}
	var kept map[string]int64
	if json.Unmarshal(b, &kept) != nil {
		return
	}
	now := d.now()
	for k, exp := range kept {
		if t := time.Unix(0, exp); t.After(now) {
			d.web.sessions[k] = t
		}
	}
}

// saveWebSessionsLocked writes the logins out, 0600; d.web.mu is held.
func (d *Daemon) saveWebSessionsLocked() {
	if d.web.file == "" {
		return
	}
	kept := map[string]int64{}
	for k, exp := range d.web.sessions {
		kept[k] = exp.UnixNano()
	}
	b, _ := json.Marshal(kept)
	tmp := d.web.file + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, d.web.file)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// webIndex exchanges a one-time token for a cookie, then serves the page.
func (d *Daemon) webIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if t := r.URL.Query().Get("t"); t != "" {
		d.web.mu.Lock()
		exp, ok := d.web.tokens[t]
		delete(d.web.tokens, t)
		if ok && d.now().Before(exp) {
			sid := randHex(24)
			d.web.sessions[sessionKey(sid)] = d.now().Add(d.web.ttl)
			d.saveWebSessionsLocked()
			http.SetCookie(w, &http.Cookie{Name: "mailai", Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
				MaxAge: int(d.web.ttl / time.Second)})
		}
		d.web.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !d.webSession(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, "mailai: no session. Run `mailai web` and open the URL it prints, or `mailai web --notify` to get the link on your phone.")
		return
	}
	b, _ := webFS.ReadFile("webui/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (d *Daemon) webSession(r *http.Request) bool {
	ck, err := r.Cookie("mailai")
	if err != nil {
		return false
	}
	key := sessionKey(ck.Value)
	d.web.mu.Lock()
	defer d.web.mu.Unlock()
	exp, ok := d.web.sessions[key]
	if !ok {
		return false
	}
	if d.now().After(exp) {
		delete(d.web.sessions, key)
		d.saveWebSessionsLocked()
		return false
	}
	return true
}

func (d *Daemon) webAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.webSession(r) {
			http.Error(w, `{"error":"no session"}`, http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if code, why := foreignWrite(r, d.web.url); code != 0 {
				http.Error(w, `{"error":"`+why+`"}`, code)
				return
			}
		}
		h(w, r)
	}
}

// foreignWrite refuses a write that did not come from the page itself.
// Cookies ignore ports, so a page on another localhost port (an agent's dev
// server) or an attachment in its sandbox could carry the session. A write
// must be JSON, which a foreign origin cannot send without a CORS preflight
// this server never grants; and when the browser says where it comes from
// (Origin, Sec-Fetch-Site), that must be this origin.
//
// public is [web] url: through the person's proxy the page's origin is
// that, and so is the origin of its writes.
func foreignWrite(r *http.Request, public string) (int, string) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return http.StatusUnsupportedMediaType, "writes are JSON"
	}
	if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host && (public == "" || o != public) {
		return http.StatusForbidden, "not from this page"
	}
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		return http.StatusForbidden, "not from this page"
	}
	return 0, ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	var pe *proto.Error
	if asProto(err, &pe) {
		switch pe.Code {
		case proto.CodeNotFound:
			code = http.StatusNotFound
		case proto.CodeNotHuman, proto.CodeForbidden:
			code = http.StatusForbidden
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func asProto(err error, target **proto.Error) bool {
	pe, ok := err.(*proto.Error)
	if ok {
		*target = pe
	}
	return ok
}

// person is a synthetic connection for web requests.
func (d *Daemon) person() *conn { return &conn{pl: webPerson} }

func (d *Daemon) apiMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, WhoamiResult{Principal: store.Human, Proof: "web"})
}

func (d *Daemon) apiInbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := d.inbox(d.person(), InboxArgs{All: q.Get("all") == "1", Closed: q.Get("closed") == "1"})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (d *Daemon) apiThreads(w http.ResponseWriter, r *http.Request) {
	res, _ := d.threads(d.person())
	writeJSON(w, res)
}

func (d *Daemon) apiThread(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	res, err := d.read(d.person(), ReadArgs{Thread: id})
	if err != nil {
		writeErr(w, err)
		return
	}
	// The thread's items, for its one-line notes (SPEC §7a).
	items, _ := d.inbox(d.person(), InboxArgs{Thread: id})
	writeJSON(w, struct {
		ReadResult
		Items []store.Item `json:"items"`
	}{res.(ReadResult), items.(InboxResult).Items})
}

func (d *Daemon) apiAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var a AnswerArgs
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, proto.Errorf(proto.CodeBadRequest, "%v", err))
		return
	}
	res, err := d.answer(d.person(), a)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (d *Daemon) apiDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var a DismissArgs
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, proto.Errorf(proto.CodeBadRequest, "%v", err))
		return
	}
	res, err := d.dismiss(d.person(), a)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

// apiDelete deletes a thread or messages as the person (§6).
func (d *Daemon) apiDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var a DeleteArgs
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, proto.Errorf(proto.CodeBadRequest, "%v", err))
		return
	}
	res, err := d.deleteMail(d.person(), a)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

// apiMarkRead is read on the person's own mailbox by id: opening a thread
// marks the person's mail in it read. Ids of other mailboxes match nothing.
func (d *Daemon) apiMarkRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var a struct {
		IDs []uint64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, proto.Errorf(proto.CodeBadRequest, "%v", err))
		return
	}
	if len(a.IDs) == 0 {
		// No ids would mean the whole mailbox to read.
		writeJSON(w, ReadResult{Messages: []store.Message{}})
		return
	}
	res, err := d.read(d.person(), ReadArgs{Mailbox: store.Human, IDs: a.IDs})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

func (d *Daemon) apiReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var a SendArgs
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, proto.Errorf(proto.CodeBadRequest, "%v", err))
		return
	}
	a.From = "" // the person, always
	res, err := d.send(d.person(), a)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
}

// apiEvents streams daemon events as SSE until the client goes away.
func (d *Daemon) apiEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", http.StatusInternalServerError)
		return
	}
	ch := make(chan proto.Event, 64)
	d.mu.Lock()
	d.webSubs[ch] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.webSubs, ch)
		d.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Event, strings.ReplaceAll(string(ev.Data), "\n", ""))
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// fanout delivers an event to socket subscribers and web streams alike.
func (d *Daemon) fanout(ev proto.Event) {
	d.mu.Lock()
	subs := make([]*conn, 0, len(d.subs))
	for c := range d.subs {
		subs = append(subs, c)
	}
	webs := make([]chan proto.Event, 0, len(d.webSubs))
	for ch := range d.webSubs {
		webs = append(webs, ch)
	}
	d.mu.Unlock()
	for _, c := range subs {
		c.write(ev)
	}
	for _, ch := range webs {
		select {
		case ch <- ev:
		default:
		}
	}
}
