package daemon

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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
	sessions map[string]time.Time // cookie -> expiry
}

type WebStartArgs struct {
	Port int `json:"port,omitempty"`
}

type WebStartResult struct {
	URL  string `json:"url"`
	Addr string `json:"addr"`
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
		return d.webStart(a.Port)
	case "web-stop":
		return d.webStop()
	case "web-revoke":
		d.web.mu.Lock()
		n := len(d.web.sessions) + len(d.web.tokens)
		d.web.sessions, d.web.tokens = map[string]time.Time{}, map[string]time.Time{}
		d.web.mu.Unlock()
		return map[string]int{"revoked": n}, nil
	}
	return nil, proto.Errorf(proto.CodeUnknownVerb, "%q", verb)
}

func (d *Daemon) webStart(port int) (any, error) {
	d.web.mu.Lock()
	defer d.web.mu.Unlock()
	if d.web.srv == nil {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, proto.Errorf(proto.CodeInternal, "listen: %v", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/", d.webIndex)
		mux.HandleFunc("/api/me", d.webAuth(d.apiMe))
		mux.HandleFunc("/api/inbox", d.webAuth(d.apiInbox))
		mux.HandleFunc("/api/threads", d.webAuth(d.apiThreads))
		mux.HandleFunc("/api/thread", d.webAuth(d.apiThread))
		mux.HandleFunc("/api/answer", d.webAuth(d.apiAnswer))
		mux.HandleFunc("/api/dismiss", d.webAuth(d.apiDismiss))
		mux.HandleFunc("/api/reply", d.webAuth(d.apiReply))
		mux.HandleFunc("/api/events", d.webAuth(d.apiEvents))
		d.web.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		d.web.ln = ln
		if d.web.tokens == nil {
			d.web.tokens, d.web.sessions = map[string]time.Time{}, map[string]time.Time{}
		}
		go d.web.srv.Serve(ln)
	}
	tok := randHex(16)
	d.web.tokens[tok] = d.now().Add(10 * time.Minute)
	addr := d.web.ln.Addr().String()
	return WebStartResult{URL: fmt.Sprintf("http://%s/?t=%s", addr, tok), Addr: addr}, nil
}

func (d *Daemon) webStop() (any, error) {
	d.web.mu.Lock()
	srv := d.web.srv
	d.web.srv, d.web.ln = nil, nil
	d.web.sessions, d.web.tokens = map[string]time.Time{}, map[string]time.Time{}
	d.web.mu.Unlock()
	if srv == nil {
		return map[string]bool{"stopped": false}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	return map[string]bool{"stopped": true}, nil
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
			d.web.sessions[sid] = d.now().Add(webSessionTTL)
			http.SetCookie(w, &http.Cookie{Name: "mailai", Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		}
		d.web.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !d.webSession(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, "mailai: no session. Run `mailai web` and open the URL it prints.")
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
	d.web.mu.Lock()
	defer d.web.mu.Unlock()
	exp, ok := d.web.sessions[ck.Value]
	if !ok {
		return false
	}
	if d.now().After(exp) {
		delete(d.web.sessions, ck.Value)
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
		h(w, r)
	}
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
	res, err := d.inbox(d.person(), InboxArgs{All: r.URL.Query().Get("all") == "1"})
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
	res, err := d.read(d.person(), ReadArgs{Thread: r.URL.Query().Get("id")})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, res)
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
