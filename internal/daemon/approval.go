package daemon

import (
	"encoding/json"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Approvals (SPEC §8, D13): a harness hook holding a permission request
// asks the daemon to put it in the Inbox and waits for the decision. The
// item carries what the harness said: tool, a short rendering of the input,
// and whether the harness called it risky. Nothing mailai invents.

type HoldApprovalArgs struct {
	Harness string          `json:"harness"`
	Tool    string          `json:"tool"`
	Input   json.RawMessage `json:"input,omitempty"`
	// Summary is one line the hook renders from the input (a command, a
	// path), for the Inbox row.
	Summary string `json:"summary"`
	Risky   bool   `json:"risky,omitempty"`
	// TimeoutMS bounds the wait; the harness's own hook timeout is the hard
	// bound and the hook should pass something shorter.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

type HoldApprovalResult struct {
	Item store.Item `json:"item"`
	// Decision is "allow" or "deny"; empty on timeout, when the harness
	// falls back to its own prompt.
	Decision string `json:"decision"`
	Message  string `json:"message,omitempty"`
	// By says who decided: human or supervisor:<name>.
	By string `json:"by,omitempty"`
}

// approvalWaiters are hooks blocked on an item id.
type approvalDecision struct {
	Decision, Message, By string
}

func (d *Daemon) holdApproval(c *conn, a HoldApprovalArgs) (any, error) {
	who := c.pl.Principal
	if who == "" {
		return nil, errForbidden("an approval hold needs a placed caller (the agent's own hook)")
	}
	// On a spoke the person decides on the inbox machine (SPEC §13a).
	if d.isSpoke() {
		return d.holdRemoteIn(c.gone, who, a)
	}
	return d.holdApprovalGone(who, a, c.gone)
}

// holdApprovalAs holds for a link, whose connection dropping is not the
// person answering anywhere.
func (d *Daemon) holdApprovalAs(who store.Address, a HoldApprovalArgs) (any, error) {
	return d.holdApprovalGone(who, a, nil)
}

// holdApprovalGone holds until a decision, the timeout, or gone closing (the
// holding hook died: answered in the session, SPEC §7a).
func (d *Daemon) holdApprovalGone(who store.Address, a HoldApprovalArgs, gone <-chan struct{}) (any, error) {
	if a.Tool == "" {
		return nil, proto.Errorf(proto.CodeBadRequest, "hold-approval needs the tool name")
	}
	summary := a.Summary
	if len(summary) > 160 {
		summary = summary[:159] + "…"
	}
	ch := make(chan approvalDecision, 1)
	d.mu.Lock()
	item, err := d.st.OpenApproval(store.Item{Kind: store.ItemApproval, From: who, Subject: a.Tool,
		Question: summary, Options: []string{"allow", "deny"}, Risky: a.Risky, Harness: a.Harness})
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	d.approvals[item.ID] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.approvals, item.ID)
		d.mu.Unlock()
	}()
	d.publishItem(item)

	var timer <-chan time.Time
	if a.TimeoutMS > 0 {
		t := time.NewTimer(time.Duration(a.TimeoutMS) * time.Millisecond)
		defer t.Stop()
		timer = t.C
	}
	select {
	case dec := <-ch:
		it, _ := d.st.Item(item.ID)
		return HoldApprovalResult{Item: it, Decision: dec.Decision, Message: dec.Message, By: dec.By}, nil
	case <-timer:
		// Nobody decided: close it so the Inbox does not show a dead prompt,
		// and let the harness fall back to its own prompt.
		it, _ := d.st.CloseItem(item.ID, "timeout", "")
		d.publishItem(it)
		return HoldApprovalResult{Item: it}, nil
	case <-gone:
		d.closeInSession(item.ID)
		return nil, errClientGone
	}
}

// decideApproval is what answer does for an approval item: it hands the
// decision to the waiting hook and closes the item. by is "human" or
// "supervisor:<name>".
func (d *Daemon) decideApproval(it store.Item, decision, message, by string) (store.Item, error) {
	d.mu.Lock()
	ch := d.approvals[it.ID]
	d.mu.Unlock()
	closed, err := d.st.CloseItemBy(it.ID, "answered", decision, by)
	if err != nil {
		return store.Item{}, proto.Errorf(proto.CodeNotFound, "no open item #%d", it.ID)
	}
	if ch != nil {
		select {
		case ch <- approvalDecision{Decision: decision, Message: message, By: by}:
		default:
		}
	}
	d.publishItem(closed)
	return closed, nil
}
