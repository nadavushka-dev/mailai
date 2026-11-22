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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/notify"
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

	// Name is this machine's name in the star (SPEC §13a).
	Name string
	// Inbox names the inbox machine (its ssh target) on a spoke; Dial opens
	// the link to it. Both empty on the inbox machine.
	Inbox string
	Dial  func(ctx context.Context) (io.ReadWriteCloser, error)
	// LinkRetry is the first reconnect wait (1 s), Heartbeat the link
	// heartbeat (15 s).
	LinkRetry time.Duration
	Heartbeat time.Duration
	// MachinesFile remembers which spokes have linked and their pinned
	// keys; memory only when "".
	MachinesFile string

	// Notifier tells the person a new Inbox item opened here (SPEC §12b);
	// nil notifies nobody. NotifyEvery is the coalescing window (3 s).
	Notifier    notify.Notifier
	NotifyEvery time.Duration
	// MachineKey is what this machine presents in link-hello (machine.key);
	// a random one for this process when nil.
	MachineKey []byte
	// ResendAfter is how long mail pushed to a spoke waits unacknowledged
	// before a heartbeat resends it (60 s).
	ResendAfter time.Duration
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
	alerts  *alerter

	agents    *agents
	web       webState
	webSubs   map[chan proto.Event]bool
	approvals map[uint64]chan approvalDecision
	marks     map[store.Address]noticeMark
	queued    []queued
	queueSeq  uint64
	insession inSessionState

	// The star (SPEC §13a).
	name        string
	inboxTarget string
	dial        func(ctx context.Context) (io.ReadWriteCloser, error)
	linkRetry   time.Duration
	heartbeat   time.Duration
	knownFile   string
	knownMu     sync.Mutex        // serializes writes of knownFile
	key         []byte            // this machine's key, sent in link-hello
	links       map[string]*conn  // inbox machine: spoke -> its link
	known       map[string]string // inbox machine: spokes that ever linked -> pinned key hash
	up          *client.Client    // spoke: the link, nil when down
	hubName     string

	mu   sync.Mutex
	subs map[*conn]bool
	// live is every open connection, closed on shutdown; stopping says the
	// daemon closed them, so a dropped connection is not the client going
	// away (§7a).
	live     map[*conn]bool
	stopping atomic.Bool
	quit     chan struct{} // closed on shutdown
	quitOnce sync.Once
	asks     *askGraph
	waits    map[string][]chan store.Message // thread id -> waiting asks
	// mailWaits are callers blocked in wait, by mailbox.
	mailWaits map[store.Address][]chan struct{}
	ln        net.Listener
	wg        sync.WaitGroup

	// linkAsks are spokes' asks held in the graph, by thread (SPEC §13a).
	linkAsks map[string][]*linkAsk
	// The spoke outbox: obMu orders sends, obKick wakes the drain, outFail
	// are queued asks waiting by thread, told if the inbox machine refuses.
	obMu    sync.Mutex
	obKick  chan struct{}
	outFail map[string][]chan error
	// spokeAsks are this spoke's ask-humans by the id --wait-for takes.
	spokeAsks map[uint64]*spokeAsk

	// Delivery to spokes (inbox machine): when each unacknowledged mid was
	// last pushed, and how long before a heartbeat resends it.
	pushed      map[string]time.Time
	resendAfter time.Duration
	// ackCalls counts link-acks received (inbox machine).
	ackCalls atomic.Int64
}

// ackWindow is how long a spoke gathers deliveries into one link-ack.
const ackWindow = 100 * time.Millisecond

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
	if o.LinkRetry == 0 {
		o.LinkRetry = time.Second
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = 15 * time.Second
	}
	if o.ResendAfter == 0 {
		o.ResendAfter = 60 * time.Second
	}
	if o.MachineKey == nil {
		o.MachineKey = newMachineKey()
	}
	d := &Daemon{st: o.Store, placer: o.Placer, now: o.Now, log: o.Log, maxAsk: o.MaxAsks, stash: o.Stash, tokens: o.Tokens,
		adapter: o.Adapter, wake: o.Wake, poll: o.PollEvery, agents: newAgents(), marks: map[store.Address]noticeMark{}, approvals: map[uint64]chan approvalDecision{}, webSubs: map[chan proto.Event]bool{},
		subs: map[*conn]bool{}, asks: newAskGraph(), linkAsks: map[string][]*linkAsk{}, obKick: make(chan struct{}, 1), outFail: map[string][]chan error{}, spokeAsks: map[uint64]*spokeAsk{},
		pushed: map[string]time.Time{}, resendAfter: o.ResendAfter, waits: map[string][]chan store.Message{}, mailWaits: map[store.Address][]chan struct{}{},
		name: o.Name, inboxTarget: o.Inbox, dial: o.Dial, linkRetry: o.LinkRetry, heartbeat: o.Heartbeat, knownFile: o.MachinesFile,
		links: map[string]*conn{}, known: map[string]string{}, key: o.MachineKey}
	if o.Notifier != nil {
		if o.NotifyEvery == 0 {
			o.NotifyEvery = 3 * time.Second
		}
		d.alerts = newAlerter(o.Notifier, o.NotifyEvery, d.itemIsOpen)
	}
	d.loadKnown()
	return d
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
		d.stopping.Store(true)
		d.quitOnce.Do(func() { close(d.quitCh()) })
		d.ln.Close()
		// Blocked clients (an ask waiting on the person, a held approval)
		// never end on their own: end their connections; their items stay
		// open for the next daemon.
		d.mu.Lock()
		for c := range d.live {
			c.Conn.Close()
		}
		d.mu.Unlock()
		d.webStop()
		if d.alerts != nil {
			d.alerts.stop()
		}
	}()
	if _, isNone := d.adapter.(adapter.None); !isNone {
		go d.pollAgents(ctx, d.poll)
	}
	go d.runWakes(ctx)
	if d.isSpoke() {
		go d.runUplink(ctx)
		go d.runOutbox(ctx)
	}
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
	// link is the spoke's machine name once this connection said link-hello.
	link string
	// gone closes when the client stops sending: it went away (SPEC §7a).
	gone chan struct{}
}

func (c *conn) write(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return proto.Write(c.Conn, v)
}

// quitCh is closed when the daemon shuts down.
func (d *Daemon) quitCh() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.quit == nil {
		d.quit = make(chan struct{})
	}
	return d.quit
}

func (d *Daemon) serveConn(nc net.Conn) {
	c := &conn{Conn: nc, pl: d.placer.Place(nc, ""), gone: make(chan struct{})}
	d.mu.Lock()
	if d.live == nil {
		d.live = map[*conn]bool{}
	}
	d.live[c] = true
	d.mu.Unlock()
	if d.stopping.Load() {
		nc.Close()
	}
	defer func() {
		d.mu.Lock()
		delete(d.subs, c)
		delete(d.live, c)
		d.mu.Unlock()
		d.dropLink(c)
		nc.Close()
	}()
	r := proto.NewReader(nc)
	var wg sync.WaitGroup
	defer func() {
		// Shutting down: blocked verbs are left to end with the process,
		// and are not told their client went away.
		if d.stopping.Load() {
			return
		}
		// Blocking verbs learn their client is gone, then are waited for;
		// one that outlives its client on purpose (a link's hold runs to
		// its timeout) is not waited for by a shutdown.
		close(c.gone)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-d.quitCh():
		}
	}()
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
		if req.Verb == "ask" || req.Verb == "ask-human" || req.Verb == "hold-approval" || req.Verb == "link-hold" || req.Verb == "wait" {
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
	// A link carries a spoke's agents, never the person (SPEC §13a): it
	// may only use the link verbs.
	if c.link != "" {
		switch req.Verb {
		case "ping", "link-send", "link-ack", "link-hold", "link-flush", "link-withdraw":
		default:
			return nil, errForbidden("a link may only use link verbs, not %q", req.Verb)
		}
	} else if strings.HasPrefix(req.Verb, "link-") && req.Verb != "link-hello" {
		return nil, errForbidden("%s needs a link", req.Verb)
	}
	switch req.Verb {
	case "ping":
		return PingResult{Pong: d.now().UTC().Format(time.RFC3339), LinkStatus: d.linkStatus()}, nil
	case "link-hello":
		a, err := decode[LinkHelloArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.linkHello(c, a)
	case "link-send":
		a, err := decode[LinkSendArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.linkSend(c, a)
	case "link-ack":
		a, err := decode[LinkAckArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.linkAck(c, a)
	case "link-hold":
		a, err := decode[LinkHoldArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.linkHold(c, a)
	case "link-flush":
		// In line: the resends go down this link before the answer.
		d.flushTo(c.link, true)
		return map[string]bool{"ok": true}, nil
	case "link-withdraw":
		a, err := decode[LinkWithdrawArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.linkWithdraw(c, a)
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
		box, err := d.box(c, a.Mailbox)
		if err != nil {
			return nil, err
		}
		n, from := d.st.Unread(box)
		return UnreadResult{Count: n, From: from}, nil
	case "wait":
		a, err := decode[WaitArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.waitMail(c, a)
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
	case "threads":
		return d.threads(c)
	case "web-start", "web-stop", "web-revoke":
		return d.webVerb(c, req.Verb, req.Args)
	case "grant":
		a, err := decode[GrantArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.grant(c, a)
	case "grant-list":
		gs := d.st.Grants()
		if gs == nil {
			gs = []store.Grant{}
		}
		return GrantListResult{Grants: gs}, nil
	case "grant-rm":
		a, err := decode[GrantRmArgs](req.Args)
		if err != nil {
			return nil, err
		}
		return d.grantRm(c, a)
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
	case "machine-list", "machine-forget":
		return d.machineVerb(c, req.Verb, req.Args)
	}
	return nil, proto.Errorf(proto.CodeUnknownVerb, "%q", req.Verb)
}

// own is the mailbox a connection reads by default.
func (d *Daemon) own(pl Placement) store.Address {
	if pl.Principal != "" {
		return pl.Principal
	}
	if !isPerson(pl) {
		// detached or a link: nobody, so no mailbox of its own (§9).
		return ""
	}
	return store.Human
}

// box is the mailbox a request means: the one named, else the caller's own.
func (d *Daemon) box(c *conn, named store.Address) (store.Address, error) {
	if named != "" {
		return named, nil
	}
	if own := d.own(c.pl); own != "" {
		return own, nil
	}
	return "", errForbidden("this caller has no mailbox of its own; pass --mailbox")
}

// PingResult is ping's answer: the time, and the machine's place in the star.
type PingResult struct {
	Pong string `json:"pong"`
	LinkStatus
}

func (d *Daemon) send(c *conn, a SendArgs) (any, error) {
	from, err := resolveFrom(c.pl, a.From)
	if err != nil {
		return nil, err
	}
	a.To = d.localize(a.To)
	if d.isSpoke() && d.offMachine(a.To) {
		if len(a.Attachments) > 0 {
			return nil, proto.Errorf(proto.CodeBadRequest, "attachments do not cross machines yet")
		}
		// Queued first, so a link that is down only delays it (SPEC §13a).
		o, err := d.st.Enqueue(store.Outgoing{From: from, To: a.To, Subject: a.Subject, Text: a.Text, ReplyTo: a.ReplyTo})
		if err != nil {
			return nil, err
		}
		r, queued, err := d.outSend(o)
		if err != nil {
			return nil, err
		}
		if queued {
			return SendResult{Message: outMessage(o), Queued: true}, nil
		}
		return SendResult{Message: r.Message}, nil
	}
	if err := d.checkRoute(a.To); err != nil {
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
	box, err := d.box(c, a.Mailbox)
	if err != nil && a.Thread == "" {
		return nil, err
	}
	if a.Thread != "" {
		msgs := d.st.Thread(a.Thread)
		if len(msgs) == 0 {
			// Any message names its thread, not only the first one.
			if m, ok := d.st.Get(a.Thread); ok && m.ThreadID != "" {
				msgs = d.st.Thread(m.ThreadID)
			}
		}
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
	var waiters []chan store.Message
	if m.Kind == store.KindReply {
		waiters = d.waits[m.ThreadID]
	}
	for _, w := range d.mailWaits[box] {
		select {
		case w <- struct{}{}:
		default:
		}
	}
	d.mu.Unlock()
	d.fanout(proto.Event{Event: "mail", Data: data})
	d.replyReleases(m)
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
	// Mail for another machine's agent goes down its link (SPEC §13a).
	d.route(m)
}

// publishItem tells subscribers an Inbox item opened or closed. It carries
// no question text: the TUI fetches the list. An item opening also tells
// the person (SPEC §12b).
func (d *Daemon) publishItem(it store.Item) {
	state := "open"
	if it.ClosedAt != 0 {
		state = "closed"
	} else {
		d.itemOpened(it)
	}
	data, _ := json.Marshal(map[string]any{"id": it.ID, "kind": it.Kind, "from": it.From, "state": state, "closed_by": it.ClosedBy,
		"maybe_in_session": it.MaybeInSessionAt != 0})
	d.fanout(proto.Event{Event: "attention", Data: data})
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
