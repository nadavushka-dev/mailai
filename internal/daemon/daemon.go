// Package daemon is the mailai process: it owns the store and serves the
// protocol on a unix socket (SPEC §4, §12).
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/stash"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Options configure a Daemon.
type Options struct {
	Store  *store.Store
	Placer Placer
	Now    func() time.Time
	Log    *log.Logger
	// MaxAsks is the bound on concurrent asks per sender (SPEC §7).
	MaxAsks int
	// Stash holds session files; nil disables the stash verbs.
	Stash *stash.Stash
}

// Daemon serves one machine.
type Daemon struct {
	st     *store.Store
	placer Placer
	now    func() time.Time
	log    *log.Logger
	maxAsk int
	stash  *stash.Stash

	mu    sync.Mutex
	subs  map[*conn]bool
	asks  *askGraph
	waits map[string][]chan store.Message // thread id -> waiting asks
	ln    net.Listener
	wg    sync.WaitGroup
}

// New makes a Daemon; Serve starts it.
func New(o Options) *Daemon {
	if o.Placer == nil {
		o.Placer = outsidePlacer{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	if o.MaxAsks == 0 {
		o.MaxAsks = 16
	}
	return &Daemon{st: o.Store, placer: o.Placer, now: o.Now, log: o.Log, maxAsk: o.MaxAsks, stash: o.Stash,
		subs: map[*conn]bool{}, asks: newAskGraph(), waits: map[string][]chan store.Message{}}
}

// Listen binds the socket (its directory 0700, stale socket removed).
func (d *Daemon) Listen(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			c.Close()
			return fmt.Errorf("a daemon already listens on %s", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	d.ln = ln
	return nil
}

// Serve accepts connections until ctx ends or the listener closes.
func (d *Daemon) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		d.ln.Close()
	}()
	for {
		c, err := d.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				d.wg.Wait()
				return nil
			}
			return err
		}
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.serveConn(c)
		}()
	}
}

type conn struct {
	net.Conn
	wmu sync.Mutex
	pl  Placement
}

func (c *conn) write(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return proto.Write(c.Conn, v)
}

func (d *Daemon) serveConn(nc net.Conn) {
	c := &conn{Conn: nc, pl: d.placer.Place(nc)}
	defer func() {
		d.mu.Lock()
		delete(d.subs, c)
		d.mu.Unlock()
		nc.Close()
	}()
	r := proto.NewReader(nc)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		f, err := r.Next()
		if err != nil {
			if err != io.EOF {
				c.write(proto.Response{Error: proto.CodeBadRequest, Hint: err.Error()})
			}
			return
		}
		if f.ID == nil || f.Verb == "" {
			c.write(proto.Response{Error: proto.CodeBadRequest, Hint: "a request needs id and verb"})
			continue
		}
		req := proto.Request{ID: *f.ID, Verb: f.Verb, Args: f.Args}
		// Blocking verbs run on their own goroutine so the connection stays
		// responsive; everything else is cheap and runs in order.
		if req.Verb == "ask" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.dispatch(c, req)
			}()
			continue
		}
		d.dispatch(c, req)
	}
}

func (d *Daemon) dispatch(c *conn, req proto.Request) {
	res, err := d.handle(c, req)
	if err != nil {
		var pe *proto.Error
		if !errors.As(err, &pe) {
			pe = mapStoreErr(err)
		}
		c.write(proto.Response{ID: req.ID, Error: pe.Code, Hint: pe.Hint})
		return
	}
	b, err := json.Marshal(res)
	if err != nil {
		c.write(proto.Response{ID: req.ID, Error: proto.CodeInternal, Hint: err.Error()})
		return
	}
	c.write(proto.Response{ID: req.ID, OK: true, Result: b})
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, proto.Errorf(proto.CodeBadRequest, "args: %v", err)
	}
	return v, nil
}

func (d *Daemon) handle(c *conn, req proto.Request) (any, error) {
	switch req.Verb {
	case "ping":
		return map[string]string{"pong": d.now().UTC().Format(time.RFC3339)}, nil
	case "whoami":
		return WhoamiResult{Principal: d.own(c.pl), Proof: c.pl.Proof, PID: c.pl.PID}, nil
	case "send":
		a, err := decode[SendArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.send(c, a)
	case "read":
		a, err := decode[ReadArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.read(c, a)
	case "unread":
		a, err := decode[UnreadArgs](req.Args)
		if err != nil {
			return nil, err
		}
		box := a.Mailbox
		if box == "" {
			box = d.own(c.pl)
		}
		n, from := d.st.Unread(box)
		return UnreadResult{Count: n, From: from}, nil
	case "ask":
		a, err := decode[AskArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.ask(c, a)
	case "subscribe":
		d.mu.Lock()
		d.subs[c] = true
		d.mu.Unlock()
		return map[string]bool{"subscribed": true}, nil
	case "stash-put", "stash-get", "stash-list":
		return d.stashVerb(req.Verb, req.Args)
	}
	return nil, proto.Errorf(proto.CodeUnknownVerb, "%q", req.Verb)
}

// own is the mailbox a connection reads by default.
func (d *Daemon) own(pl Placement) store.Address {
	if pl.Principal != "" {
		return pl.Principal
	}
	return store.Human
}

func (d *Daemon) send(c *conn, a SendArgs) (any, error) {
	from, err := resolveFrom(c.pl, a.From)
	if err != nil {
		return nil, err
	}
	atts, err := d.prepareAttachments(a.Attachments)
	if err != nil {
		return nil, err
	}
	m, err := d.store(c.pl, store.SendRequest{
		From: from, FromLabel: a.FromLabel, To: a.To, Session: a.Session,
		Subject: a.Subject, Text: a.Text, ReplyTo: a.ReplyTo, Attachments: atts,
		Proof: c.pl.Proof,
	})
	if err != nil {
		return nil, err
	}
	return SendResult{Message: m}, nil
}

// store puts a message in and tells everyone who waits on it.
func (d *Daemon) store(pl Placement, r store.SendRequest) (store.Message, error) {
	m, err := d.st.Send(r)
	if err != nil {
		return m, err
	}
	d.publish(m)
	return m, nil
}

func (d *Daemon) read(c *conn, a ReadArgs) (any, error) {
	box := a.Mailbox
	if box == "" {
		box = d.own(c.pl)
	}
	if a.Thread != "" {
		msgs := d.st.Thread(a.Thread)
		resolveMissing(msgs)
		if msgs == nil {
			msgs = []store.Message{}
		}
		return ReadResult{Messages: msgs}, nil
	}
	peekForced := false
	// SPEC §9: a pane reading the person's inbox never clears it.
	if box == store.Human && c.pl.Proof != store.ProofOutsidePane && !a.Peek {
		a.Peek = true
		peekForced = true
	}
	var msgs []store.Message
	var err error
	if a.Peek {
		msgs, err = d.st.Peek(store.PeekQuery{Mailbox: box, By: d.own(c.pl), Unread: a.Unread})
	} else {
		msgs, err = d.st.Read(store.ReadQuery{Mailbox: box, Unread: a.Unread, IDs: a.IDs, Mark: true})
	}
	if err != nil {
		return nil, err
	}
	resolveMissing(msgs)
	if msgs == nil {
		msgs = []store.Message{}
	}
	return ReadResult{Messages: msgs, PeekForced: peekForced}, nil
}

func (d *Daemon) publish(m store.Message) {
	box := m.To
	if box == "" {
		box = store.NoticeBox(m.Session)
	}
	ev := MailEvent{MID: m.MID, Kind: m.Kind, Mailbox: box, From: m.From, Thread: m.ThreadID, ReplyTo: m.ReplyTo}
	data, _ := json.Marshal(ev)
	d.mu.Lock()
	subs := make([]*conn, 0, len(d.subs))
	for c := range d.subs {
		subs = append(subs, c)
	}
	var waiters []chan store.Message
	if m.Kind == store.KindReply {
		waiters = d.waits[m.ThreadID]
	}
	d.mu.Unlock()
	for _, c := range subs {
		c.write(proto.Event{Event: "mail", Data: data})
	}
	for _, w := range waiters {
		select {
		case w <- m:
		default:
		}
	}
}

func errForbidden(format string, a ...any) error {
	return proto.Errorf(proto.CodeForbidden, format, a...)
}

func mapStoreErr(err error) *proto.Error {
	switch {
	case errors.Is(err, store.ErrMailboxFull):
		return &proto.Error{Code: proto.CodeMailboxFull, Hint: "the recipient's unread mail fills its mailbox"}
	case errors.Is(err, store.ErrRateLimited):
		return &proto.Error{Code: proto.CodeRateLimited, Hint: "slow down: 10 messages burst, one per second after"}
	default:
		return &proto.Error{Code: proto.CodeBadRequest, Hint: err.Error()}
	}
}
