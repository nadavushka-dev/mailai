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

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/stash"
	"github.com/nadavushka-dev/mailai/internal/store"
	"github.com/nadavushka-dev/mailai/internal/tokens"
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
	// Tokens is the token file; nil disables the token verbs.
	Tokens *tokens.Tokens
	// Adapter says which agents exist and types at them; None when nil.
	Adapter adapter.Adapter
	// Wake enables typing the notice at resting agents ([mail] wake).
	Wake bool
	// PollEvery is how often the adapter is asked; 2 s when zero.
	PollEvery time.Duration
}

// Daemon serves one machine.
type Daemon struct {
	st      *store.Store
	placer  Placer
	now     func() time.Time
	log     *log.Logger
	maxAsk  int
	stash   *stash.Stash
	tokens  *tokens.Tokens
	adapter adapter.Adapter
	wake    bool
	poll    time.Duration

	agents    *agents
	approvals map[uint64]chan approvalDecision
	marks     map[store.Address]noticeMark
	queued    []queued
	queueSeq  uint64

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
	if o.Adapter == nil {
		o.Adapter = adapter.None{}
	}
	if o.PollEvery == 0 {
		o.PollEvery = 2 * time.Second
	}
	return &Daemon{st: o.Store, placer: o.Placer, now: o.Now, log: o.Log, maxAsk: o.MaxAsks, stash: o.Stash, tokens: o.Tokens,
		adapter: o.Adapter, wake: o.Wake, poll: o.PollEvery, agents: newAgents(), marks: map[store.Address]noticeMark{}, approvals: map[uint64]chan approvalDecision{},
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

// Serve accepts connections until ctx ends or the listener closes, and runs
// the adapter poll, wakes and queue delivery meanwhile.
func (d *Daemon) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		d.ln.Close()
	}()
	if _, isNone := d.adapter.(adapter.None); !isNone {
		go d.pollAgents(ctx, d.poll)
	}
	go d.runWakes(ctx)
	go func() {
		t := time.NewTicker(wakeTick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				d.deliverQueued(ctx)
			}
		}
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
	c := &conn{Conn: nc, pl: d.placer.Place(nc, "")}
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
		if req.Verb == "ask" || req.Verb == "ask-human" || req.Verb == "hold-approval" {
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
	case "hello":
		a, err := decode[HelloArgs](req.Args)
		if err != nil {
			return nil, err
		}
		c.pl = d.placer.Place(c.Conn, a.Token)
		return WhoamiResult{Principal: d.own(c.pl), Proof: c.pl.Proof, PID: c.pl.PID}, nil
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
	case "ask-human":
		a, err := decode[AskHumanArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.askHuman(c, a)
	case "inbox":
		a, err := decode[InboxArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.inbox(c, a)
	case "answer":
		a, err := decode[AnswerArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.answer(c, a)
	case "dismiss":
		a, err := decode[DismissArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.dismiss(c, a)
	case "hold-approval":
		a, err := decode[HoldApprovalArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.holdApproval(c, a)
	case "notice":
		a, err := decode[NoticeArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.notice(c, a)
	case "report-state":
		a, err := decode[ReportStateArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.reportState(c, a)
	case "agents":
		return d.listAgents(), nil
	case "queue":
		a, err := decode[QueueArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.queue(c, a)
	case "queue-list":
		return d.queueList(), nil
	case "queue-rm":
		a, err := decode[QueueRmArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.queueRm(c, a)
	case "subscribe":
		d.mu.Lock()
		d.subs[c] = true
		d.mu.Unlock()
		return map[string]bool{"subscribed": true}, nil
	case "stash-put", "stash-get", "stash-list":
		return d.stashVerb(req.Verb, req.Args)
	case "token-new", "token-list", "token-revoke":
		return d.tokenVerb(c, req.Verb, req.Args)
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
		Proof: c.pl.Proof, Verified: verifiedFor(c.pl, from),
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
	if box == store.Human && !isPerson(c.pl) && !a.Peek {
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
	// An ask to human opened an item; a verified reply may have closed one.
	if m.To == store.Human && (m.Kind == store.KindAsk || m.Kind == store.KindAskHuman) {
		for _, it := range d.st.Items(false) {
			if it.MID == m.MID {
				d.publishItem(it)
			}
		}
	}
	if m.Kind == store.KindReply && m.Verified != "" {
		for _, it := range d.st.Items(true) {
			if it.Answer == m.MID {
				d.publishItem(it)
			}
		}
	}
}

// publishItem tells subscribers an Inbox item opened or closed. It carries
// no question text: the TUI fetches the list.
func (d *Daemon) publishItem(it store.Item) {
	state := "open"
	if it.ClosedAt != 0 {
		state = "closed"
	}
	data, _ := json.Marshal(map[string]any{"id": it.ID, "kind": it.Kind, "from": it.From, "state": state, "closed_by": it.ClosedBy})
	d.mu.Lock()
	subs := make([]*conn, 0, len(d.subs))
	for c := range d.subs {
		subs = append(subs, c)
	}
	d.mu.Unlock()
	for _, c := range subs {
		c.write(proto.Event{Event: "attention", Data: data})
	}
}

// verifiedFor is the one place "verified" is set (SPEC §9): the person's
// own sends, from outside every pane. Supervisors come with grants (M4).
func verifiedFor(pl Placement, from store.Address) string {
	if from == store.Human && isPerson(pl) {
		return "human"
	}
	return ""
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
