package daemon

import (
	"strings"
	"time"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Answered in the session (SPEC §7a). The person sometimes answers an
// agent in its own pane instead of the Inbox. What mailai can be sure of
// closes the item as in_session:
//   - the blocking ask or ask-human connection died before a reply (the
//     person pressed Esc in the harness, which killed the CLI);
//   - a held approval's tool ran, the agent's turn ended, or the holding
//     hook died (the person answered the harness's own dialog); the hook is
//     let go with no decision so the harness keeps what the person chose.
// What is only likely (a new prompt, or Herdr's blocked -> working while an
// ask is open) marks the item maybe_in_session_at and leaves it open.
// Nothing here reaches an agent's context (D12).

// Signals a harness hook reports with its state, mapped from the harness's
// own events by its manifest (D14).
const (
	SignalPrompt    = "prompt"     // the agent got a new prompt
	SignalToolRan   = "tool_ran"   // a tool finished running
	SignalTurnEnded = "turn_ended" // the agent's turn ended
)

func validSignal(s string) bool {
	switch s {
	case "", SignalPrompt, SignalToolRan, SignalTurnEnded:
		return true
	}
	return false
}

// MaybeBadge is how an open item marked maybe_in_session_at is shown.
const MaybeBadge = "maybe answered in session"

// ClosedHow says in a few words how an item closed, for the person: by you,
// by supervisor:<name>, in session, dismissed, timeout.
func ClosedHow(it store.Item) string {
	super := strings.HasPrefix(it.DecidedBy, "supervisor:")
	switch it.ClosedBy {
	case "":
		return ""
	case store.ClosedInSession:
		return "in session"
	case "answered":
		if super {
			return "by " + it.DecidedBy
		}
		return "by you"
	case "dismissed":
		if super {
			return "dismissed by " + it.DecidedBy
		}
		return "dismissed"
	}
	return it.ClosedBy
}

// errClientGone ends a blocking verb whose client went away; nobody reads it.
var errClientGone = proto.Errorf(proto.CodeInternal, "the client went away")

// LinkWithdrawArgs is a spoke telling the inbox machine that one of its
// agents' items was answered in the session. MID names an ask's item; From
// (a bare local name) with Tool names held approvals, every tool when Tool
// is empty (the turn ended).
type LinkWithdrawArgs struct {
	MID  string        `json:"mid,omitempty"`
	From store.Address `json:"from,omitempty"`
	Tool string        `json:"tool,omitempty"`
}

type LinkWithdrawResult struct {
	Closed int `json:"closed"`
}

// inSessionState is the spoke's count of approvals held on the inbox
// machine per agent, so a tool_ran or turn_ended is relayed only when one
// could be waiting.
type inSessionState struct {
	held map[store.Address]int
}

// closeInSession closes one open item as in_session and tells subscribers.
func (d *Daemon) closeInSession(id uint64) (store.Item, bool) {
	it, err := d.st.CloseItem(id, store.ClosedInSession, "")
	if err != nil {
		return store.Item{}, false
	}
	d.publishItem(it)
	return it, true
}

// closeAskInSession closes the open item an ask opened, if any.
func (d *Daemon) closeAskInSession(mid string) {
	for _, it := range d.st.Items(false) {
		if it.MID == mid {
			d.closeInSession(it.ID)
		}
	}
}

// approvalsEnded closes who's held approvals for tool (every tool when "")
// as in_session and releases their hooks with no decision.
func (d *Daemon) approvalsEnded(who store.Address, tool string) int {
	n := 0
	for _, it := range d.st.Items(false) {
		if it.Kind != store.ItemApproval || it.From != who || (tool != "" && it.Subject != tool) {
			continue
		}
		d.mu.Lock()
		ch := d.approvals[it.ID]
		d.mu.Unlock()
		if _, ok := d.closeInSession(it.ID); !ok {
			continue
		}
		n++
		if ch != nil {
			select {
			case ch <- approvalDecision{}:
			default:
			}
		}
	}
	return n
}

// onSignal acts on what an agent's own hook said; who comes from the
// connection, never the request.
func (d *Daemon) onSignal(who store.Address, signal, tool string) {
	switch signal {
	case SignalPrompt:
		d.markMaybe(who)
	case SignalToolRan:
		if tool != "" {
			d.sessionAnswered(who, tool)
		}
	case SignalTurnEnded:
		d.sessionAnswered(who, "")
	}
}

func (d *Daemon) sessionAnswered(who store.Address, tool string) {
	if !d.isSpoke() {
		d.approvalsEnded(who, tool)
		return
	}
	d.mu.Lock()
	held := d.insession.held[who]
	d.mu.Unlock()
	if held > 0 {
		d.withdrawRemote(LinkWithdrawArgs{From: who, Tool: tool})
	}
}

func (d *Daemon) markMaybe(who store.Address) {
	for _, it := range d.st.MarkMaybeInSession(who) {
		d.publishItem(it)
	}
}

// holdRemoteIn holds a spoke's approval on the inbox machine, and withdraws
// it there when the holding hook goes away.
func (d *Daemon) holdRemoteIn(gone <-chan struct{}, who store.Address, a HoldApprovalArgs) (any, error) {
	d.mu.Lock()
	if d.insession.held == nil {
		d.insession.held = map[store.Address]int{}
	}
	d.insession.held[who]++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.insession.held[who]--; d.insession.held[who] <= 0 {
			delete(d.insession.held, who)
		}
		d.mu.Unlock()
	}()
	type res struct {
		v   any
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := d.holdRemote(who, a)
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-gone:
		d.withdrawRemote(LinkWithdrawArgs{From: who, Tool: a.Tool})
		return nil, errClientGone
	}
}

// withdrawRemote tells the inbox machine, retrying while the link is down
// for up to an hour; the item stays open if it never comes back.
func (d *Daemon) withdrawRemote(a LinkWithdrawArgs) {
	go func() {
		wait := d.linkRetry
		give := time.Now().Add(time.Hour)
		for {
			if cl := d.uplink(); cl != nil {
				err := cl.Call("link-withdraw", a, nil)
				if err == nil {
					return
				}
				if client.Code(err) != "" {
					d.log.Printf("link-withdraw %+v: %v", a, err)
					return
				}
			}
			if time.Now().After(give) {
				d.log.Printf("link-withdraw %+v: link down for an hour; giving up", a)
				return
			}
			time.Sleep(wait)
			if wait *= 2; wait > 60*time.Second {
				wait = 60 * time.Second
			}
		}
	}()
}

// linkWithdraw is the inbox machine's side: a link may close only items
// whose asker is one of its own machine's agents (SPEC §13a).
func (d *Daemon) linkWithdraw(c *conn, a LinkWithdrawArgs) (any, error) {
	if a.MID == "" {
		from, err := linkFrom(c, a.From)
		if err != nil {
			return nil, err
		}
		return LinkWithdrawResult{Closed: d.approvalsEnded(from, a.Tool)}, nil
	}
	for _, it := range d.st.Items(true) {
		if it.MID != a.MID {
			continue
		}
		if name, host := splitAddr(it.From); name == "" || host != c.link {
			return nil, errForbidden("a link withdraws only its own machine's items")
		}
		if it.ClosedAt != 0 {
			return LinkWithdrawResult{}, nil
		}
		if _, ok := d.closeInSession(it.ID); ok {
			return LinkWithdrawResult{Closed: 1}, nil
		}
		return LinkWithdrawResult{}, nil
	}
	return LinkWithdrawResult{}, nil
}
