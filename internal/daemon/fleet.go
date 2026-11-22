package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// The star (SPEC §13a): every machine runs a daemon; one is the inbox
// machine, and each spoke holds one link to it. The inbox machine's side is
// a connection that said link-hello; the spoke's side is a client over the
// link (ssh to `mailai bridge` in production, a socket in tests).

type LinkHelloArgs struct {
	Machine string `json:"machine"`
	Version string `json:"version,omitempty"`
	// Key is the spoke's machine key; the inbox machine pins its hash.
	Key []byte `json:"key,omitempty"`
}

type LinkHelloResult struct {
	Machine string `json:"machine"`
}

// LinkSendArgs is a spoke's agent sending off its machine. From is the bare
// local name; the inbox machine qualifies it with the link's machine.
type LinkSendArgs struct {
	From    store.Address `json:"from"`
	To      store.Address `json:"to"`
	Kind    store.Kind    `json:"kind,omitempty"`
	Subject string        `json:"subject,omitempty"`
	Text    string        `json:"text"`
	ReplyTo string        `json:"reply_to,omitempty"`
	Options []string      `json:"options,omitempty"`
	// TimeoutMS is how long an ask waits on the spoke; the inbox machine
	// holds the ask's edge in its graph that long, 24 h at most.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

type LinkSendResult struct {
	Message store.Message `json:"message"`
	// Item is the Inbox item the send opened, for an ask to human.
	Item *store.Item `json:"item,omitempty"`
}

type LinkAckArgs struct {
	MIDs []string `json:"mids"`
}

type LinkHoldArgs struct {
	From store.Address    `json:"from"`
	Hold HoldApprovalArgs `json:"hold"`
}

var machineName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// splitAddr is "name@machine" -> name, machine; machine is "" when bare.
func splitAddr(a store.Address) (string, string) {
	s := string(a)
	if i := strings.LastIndexByte(s, '@'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// localize drops "@<this machine>" so a self-addressed send is local.
func (d *Daemon) localize(a store.Address) store.Address {
	if name, m := splitAddr(a); m != "" && m == d.name {
		return store.Address(name)
	}
	return a
}

// isSpoke says this daemon sends off the machine through an inbox machine.
func (d *Daemon) isSpoke() bool { return d.dial != nil }

// offMachine says a (localized) address is not on this machine. On a spoke
// that includes the person, who answers on the inbox machine.
func (d *Daemon) offMachine(to store.Address) bool {
	if to == "" {
		return false
	}
	if _, m := splitAddr(to); m != "" {
		return true
	}
	return d.isSpoke() && to == store.Human
}

// checkRoute refuses what this daemon cannot deliver (SPEC §13a): on the
// inbox machine, a machine that never linked.
func (d *Daemon) checkRoute(to store.Address) error {
	_, m := splitAddr(to)
	if m == "" || d.isSpoke() {
		return nil
	}
	d.mu.Lock()
	_, known := d.known[m]
	d.mu.Unlock()
	if !known {
		return proto.Errorf(proto.CodeOffline, "%s is on machine %q, which has never linked to this inbox machine", to, m)
	}
	return nil
}

// ---- the inbox machine's side ----

func (d *Daemon) linkHello(c *conn, a LinkHelloArgs) (any, error) {
	// The bridge runs under ssh -T: outside every pane, usually no terminal.
	if (c.pl.Proof != store.ProofOutsidePane && c.pl.Proof != store.ProofDetached) || c.link != "" {
		return nil, errForbidden("only a bridge from outside every pane may open a link")
	}
	if !machineName.MatchString(a.Machine) || a.Machine == d.name {
		return nil, proto.Errorf(proto.CodeBadRequest, "bad machine name %q", a.Machine)
	}
	if len(a.Key) != 0 && len(a.Key) != MachineKeySize {
		return nil, proto.Errorf(proto.CodeBadRequest, "a machine key is %d bytes", MachineKeySize)
	}
	d.mu.Lock()
	save, err := d.checkPin(a.Machine, a.Key)
	if err != nil {
		d.mu.Unlock()
		d.log.Printf("link refused: %s presented another machine key", a.Machine)
		return nil, err
	}
	c.link = a.Machine
	c.pl = Placement{Proof: store.ProofLink}
	old := d.links[a.Machine]
	d.links[a.Machine] = c
	d.mu.Unlock()
	if old != nil && old != c {
		old.Close()
	}
	if save {
		d.saveKnown()
	}
	d.log.Printf("link up: %s", a.Machine)
	go d.flushTo(a.Machine)
	return LinkHelloResult{Machine: d.name}, nil
}

// linkFrom qualifies a spoke's sender: a bare local name, never the person.
func linkFrom(c *conn, claimed store.Address) (store.Address, error) {
	if claimed == "" || claimed == store.Human || strings.Contains(string(claimed), "@") {
		return "", errForbidden("a link speaks only for its own machine's agents, not %q", claimed)
	}
	return store.Address(string(claimed) + "@" + c.link), nil
}

func (d *Daemon) linkSend(c *conn, a LinkSendArgs) (any, error) {
	from, err := linkFrom(c, a.From)
	if err != nil {
		return nil, err
	}
	to := d.localize(a.To)
	if to == "" {
		return nil, proto.Errorf(proto.CodeBadRequest, "a link send needs a recipient")
	}
	if err := d.checkRoute(to); err != nil {
		return nil, err
	}
	switch a.Kind {
	case "", store.KindMail, store.KindReply, store.KindAsk:
	case store.KindAskHuman:
		if to != store.Human || len(a.Options) > MaxOptions {
			return nil, proto.Errorf(proto.CodeBadRequest, "ask_human goes to human with at most %d options", MaxOptions)
		}
	default:
		return nil, proto.Errorf(proto.CodeBadRequest, "kind %q does not cross machines", a.Kind)
	}
	if a.Kind == store.KindAsk {
		if err := d.openLinkAsk(from, to); err != nil {
			return nil, err
		}
	}
	m, err := d.st.Send(store.SendRequest{From: from, To: to, Kind: a.Kind, Subject: a.Subject, Text: a.Text,
		ReplyTo: a.ReplyTo, Options: a.Options, Remote: true, Proof: store.ProofLink, Origin: "link", OriginHost: c.link})
	if err != nil {
		if a.Kind == store.KindAsk {
			d.releaseAsk(from, to)
		}
		return nil, err
	}
	if a.Kind == store.KindAsk {
		d.holdLinkAsk(m, linkAskTimeout(a.TimeoutMS))
	}
	d.publish(m)
	res := LinkSendResult{Message: m}
	for _, it := range d.st.Items(false) {
		if it.MID == m.MID {
			it := it
			res.Item = &it
		}
	}
	return res, nil
}

func (d *Daemon) linkAck(c *conn, a LinkAckArgs) (any, error) {
	n := 0
	for _, mid := range a.MIDs {
		m, ok := d.st.Get(mid)
		if !ok {
			continue
		}
		if _, host := splitAddr(m.To); host != c.link {
			continue
		}
		if _, err := d.st.Read(store.ReadQuery{Mailbox: m.To, IDs: []uint64{m.ID}, Mark: true}); err == nil {
			n++
		}
	}
	return map[string]int{"acked": n}, nil
}

func (d *Daemon) linkHold(c *conn, a LinkHoldArgs) (any, error) {
	from, err := linkFrom(c, a.From)
	if err != nil {
		return nil, err
	}
	return d.holdApprovalAs(from, a.Hold)
}

// route pushes a message for name@machine down that machine's link, if up.
// It stays unread in its box until the spoke acknowledges it.
func (d *Daemon) route(m store.Message) {
	_, host := splitAddr(m.To)
	if host == "" || d.isSpoke() {
		return
	}
	d.mu.Lock()
	c := d.links[host]
	d.mu.Unlock()
	if c == nil {
		return
	}
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	c.write(proto.Event{Event: "deliver", Data: data})
}

// flushTo resends everything a spoke has not acknowledged, oldest first.
func (d *Daemon) flushTo(machine string) {
	var pending []store.Message
	for _, box := range d.st.Mailboxes() {
		if _, host := splitAddr(box); host != machine {
			continue
		}
		msgs, _ := d.st.Peek(store.PeekQuery{Mailbox: box, Unread: true})
		pending = append(pending, msgs...)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })
	for _, m := range pending {
		d.route(m)
	}
}

func (d *Daemon) dropLink(c *conn) {
	if c.link == "" {
		return
	}
	d.mu.Lock()
	if d.links[c.link] == c {
		delete(d.links, c.link)
	}
	d.mu.Unlock()
	d.log.Printf("link down: %s", c.link)
}

func (d *Daemon) hasLink(machine string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.links[machine] != nil
}

// ---- the spoke's side ----

func (d *Daemon) uplink() *client.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.up
}

func (d *Daemon) linkUp() bool { return d.uplink() != nil }

func (d *Daemon) errOffline() error {
	return proto.Errorf(proto.CodeOffline, "the link to the inbox machine %s is down; try again when `mailai ping` says it is up", d.inboxTarget)
}

// relay sends over the link; a dropped link is offline, not internal.
func (d *Daemon) relay(a LinkSendArgs) (LinkSendResult, error) {
	var r LinkSendResult
	cl := d.uplink()
	if cl == nil {
		return r, d.errOffline()
	}
	if err := cl.Call("link-send", a, &r); err != nil {
		if client.Code(err) == "" {
			return r, d.errOffline()
		}
		return r, err
	}
	return r, nil
}

// heldReply finds a reply to from already delivered in thread, for an ask
// that registered its wait after the reply landed.
func (d *Daemon) heldReply(thread string, from store.Address) (store.Message, bool) {
	for _, m := range d.st.Thread(thread) {
		if m.Kind == store.KindReply && m.To == from && m.ReadAt == 0 {
			return m, true
		}
	}
	return store.Message{}, false
}

// waitThread blocks for a reply to from in thread, or the timeout.
func (d *Daemon) waitThread(ch chan store.Message, thread string, from store.Address, timeout time.Duration, what string) (store.Message, error) {
	if m, ok := d.heldReply(thread, from); ok {
		d.st.Read(store.ReadQuery{Mailbox: from, IDs: []uint64{m.ID}, Mark: true})
		return m, nil
	}
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	for {
		select {
		case r := <-ch:
			if r.To == from {
				d.st.Read(store.ReadQuery{Mailbox: from, IDs: []uint64{r.ID}, Mark: true})
				return r, nil
			}
		case <-timer:
			return store.Message{}, proto.Errorf(proto.CodeTimeout, "no reply %s within %s", what, timeout)
		}
	}
}

func (d *Daemon) registerWait(thread string) chan store.Message {
	ch := make(chan store.Message, 1)
	d.mu.Lock()
	d.waits[thread] = append(d.waits[thread], ch)
	d.mu.Unlock()
	return ch
}

// askRemote is an ask that leaves the machine: sent over the link without
// holding it, then waited for here, so a dropped link does not end it.
func (d *Daemon) askRemote(from store.Address, a AskArgs) (any, error) {
	if err := d.checkAsk(from, a.To); err != nil {
		return nil, err
	}
	defer d.releaseAsk(from, a.To)
	r, err := d.relay(LinkSendArgs{From: from, To: a.To, Kind: store.KindAsk, Subject: a.Subject, Text: a.Text, TimeoutMS: a.TimeoutMS})
	if err != nil {
		return nil, err
	}
	ch := d.registerWait(r.Message.ThreadID)
	defer d.unwait(r.Message.ThreadID, ch)
	reply, err := d.waitThread(ch, r.Message.ThreadID, from, time.Duration(a.TimeoutMS)*time.Millisecond, "from "+string(a.To))
	if err != nil {
		return nil, err
	}
	return AskResult{Ask: r.Message, Reply: reply}, nil
}

func (d *Daemon) askHumanRemote(from store.Address, a AskHumanArgs) (any, error) {
	if a.WaitFor != 0 {
		return nil, proto.Errorf(proto.CodeBadRequest, "--wait-for is not supported on a spoke; the answer arrives as mail")
	}
	if err := d.checkAsk(from, store.Human); err != nil {
		return nil, err
	}
	defer d.releaseAsk(from, store.Human)
	r, err := d.relay(LinkSendArgs{From: from, To: store.Human, Kind: store.KindAskHuman, Subject: a.Subject, Text: a.Text, Options: a.Options})
	if err != nil {
		return nil, err
	}
	var item store.Item
	if r.Item != nil {
		item = *r.Item
	}
	if a.NoWait {
		return AskHumanResult{Item: item}, nil
	}
	ch := d.registerWait(r.Message.ThreadID)
	defer d.unwait(r.Message.ThreadID, ch)
	reply, err := d.waitThread(ch, r.Message.ThreadID, from, time.Duration(a.TimeoutMS)*time.Millisecond, "from the person")
	if err != nil {
		return nil, err
	}
	item.ClosedBy, item.Answer = "answered", reply.MID
	return AskHumanResult{Item: item, Reply: &reply}, nil
}

func (d *Daemon) holdRemote(from store.Address, a HoldApprovalArgs) (any, error) {
	cl := d.uplink()
	if cl == nil {
		return nil, d.errOffline()
	}
	var r HoldApprovalResult
	if err := cl.Call("link-hold", LinkHoldArgs{From: from, Hold: a}, &r); err != nil {
		if client.Code(err) == "" {
			return nil, d.errOffline()
		}
		return nil, err
	}
	return r, nil
}

// accept stores what the inbox machine delivered, in the local agent's box.
func (d *Daemon) accept(m store.Message) {
	name, host := splitAddr(m.To)
	if host != d.name || name == "" || name == string(store.Human) {
		return
	}
	m.To = store.Address(name)
	m.Origin = "link"
	d.mu.Lock()
	hub := d.hubName
	d.mu.Unlock()
	// The inbox machine's own agents are bare there; here they are name@hub,
	// so a reply from this machine finds its way back. The person stays human.
	if _, h := splitAddr(m.From); h != "" {
		m.OriginHost = h
	} else {
		m.OriginHost = hub
		if m.From != store.Human && hub != "" {
			m.From = store.Address(string(m.From) + "@" + hub)
		}
	}
	stored, fresh, err := d.st.Accept(m.To, m)
	if err != nil {
		d.log.Printf("accept %s: %v", m.MID, err)
		return
	}
	if fresh {
		d.publish(stored)
	}
}

// runUplink keeps the spoke's link to the inbox machine up, with backoff.
func (d *Daemon) runUplink(ctx context.Context) {
	wait := d.linkRetry
	for ctx.Err() == nil {
		rwc, err := d.dial(ctx)
		if err == nil {
			var up bool
			up, err = d.serveUplink(ctx, rwc)
			if up {
				wait = d.linkRetry
			}
		}
		if ctx.Err() != nil {
			return
		}
		d.log.Printf("link to %s: %v; retry in %s", d.inboxTarget, err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > 60*time.Second {
			wait = 60 * time.Second
		}
	}
}

// callWithin bounds a call on the link; a link that does not answer is dead.
func callWithin(cl *client.Client, d time.Duration, verb string, args, out any) error {
	done := make(chan error, 1)
	go func() { done <- cl.Call(verb, args, out) }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		cl.Close()
		return fmt.Errorf("%s: no answer in %s", verb, d)
	}
}

func (d *Daemon) serveUplink(ctx context.Context, rwc io.ReadWriteCloser) (bool, error) {
	cl, err := client.New(rwc, "")
	if err != nil {
		rwc.Close()
		return false, err
	}
	defer cl.Close()
	var hr LinkHelloResult
	if err := callWithin(cl, 15*time.Second, "link-hello", LinkHelloArgs{Machine: d.name, Key: d.key}, &hr); err != nil {
		return false, err
	}
	d.mu.Lock()
	d.up = cl
	d.hubName = hr.Machine
	d.mu.Unlock()
	d.log.Printf("link up: inbox machine %s via %s", hr.Machine, d.inboxTarget)
	defer func() {
		d.mu.Lock()
		if d.up == cl {
			d.up = nil
		}
		d.mu.Unlock()
	}()
	beat := time.NewTicker(d.heartbeat)
	defer beat.Stop()
	events := cl.Events()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case f, ok := <-events:
			if !ok {
				return true, errors.New("link closed")
			}
			if f.Event != "deliver" {
				continue
			}
			var m store.Message
			if json.Unmarshal(f.Data, &m) != nil {
				continue
			}
			d.accept(m)
			go cl.Call("link-ack", LinkAckArgs{MIDs: []string{m.MID}}, nil)
		case <-beat.C:
			// The heartbeat also asks for anything not yet acknowledged.
			if err := callWithin(cl, 3*d.heartbeat, "link-flush", nil, nil); err != nil {
				return true, err
			}
		}
	}
}

// LinkStatus is what ping reports about the fleet.
type LinkStatus struct {
	Machine string   `json:"machine,omitempty"`
	Inbox   string   `json:"inbox,omitempty"`
	Link    string   `json:"link,omitempty"`
	Links   []string `json:"links,omitempty"`
}

func (d *Daemon) linkStatus() LinkStatus {
	s := LinkStatus{Machine: d.name}
	if d.isSpoke() {
		s.Inbox = d.inboxTarget
		s.Link = "down"
		if d.linkUp() {
			s.Link = "up"
		}
		return s
	}
	d.mu.Lock()
	for m := range d.links {
		s.Links = append(s.Links, m)
	}
	d.mu.Unlock()
	sort.Strings(s.Links)
	return s
}
