package daemon

import (
	"context"
	"sort"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// agents is what the daemon knows about live agents: the adapter's snapshot
// merged with what harness hooks report. A hook report is fresher than a
// poll, so it wins until the next poll disagrees with a newer timestamp.
type agents struct {
	byName map[store.Address]*agentRec
}

type agentRec struct {
	adapter.Agent
	ReportedAt int64 `json:"reported_at"`
	// Source says who said so last: "adapter" or "hook".
	Source string `json:"source"`
}

func newAgents() *agents { return &agents{byName: map[store.Address]*agentRec{}} }

// ReportStateArgs is what a harness hook sends (SPEC §8): its own state.
type ReportStateArgs struct {
	State   adapter.State `json:"state"`
	Harness string        `json:"harness,omitempty"`
	Session string        `json:"session,omitempty"`
	Label   string        `json:"label,omitempty"`
}

type AgentsResult struct {
	Adapter string     `json:"adapter"`
	Agents  []agentRec `json:"agents"`
}

// pollAgents refreshes the registry from the adapter until ctx ends.
func (d *Daemon) pollAgents(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		d.refreshAgents(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) refreshAgents(ctx context.Context) {
	list, err := d.adapter.Agents(ctx)
	if err != nil {
		d.log.Printf("adapter %s: %v", d.adapter.Name(), err)
		return
	}
	now := d.now().UnixNano()
	d.mu.Lock()
	defer d.mu.Unlock()
	seen := map[store.Address]bool{}
	for _, a := range list {
		seen[a.Principal] = true
		rec := d.agents.byName[a.Principal]
		if rec == nil {
			rec = &agentRec{}
			d.agents.byName[a.Principal] = rec
		}
		// A hook report in the last 2 s is fresher than this poll.
		if rec.Source == "hook" && now-rec.ReportedAt < int64(2*time.Second) {
			rec.PaneID, rec.Session = a.PaneID, a.Session
			if rec.Label == "" {
				rec.Label = a.Label
			}
			continue
		}
		hookHarness := rec.Harness
		rec.Agent = a
		if a.Harness == "" {
			rec.Harness = hookHarness
		}
		rec.ReportedAt, rec.Source = now, "adapter"
	}
	// An agent the adapter no longer lists, and no hook has reported for
	// 10 minutes, is gone.
	for name, rec := range d.agents.byName {
		if !seen[name] && rec.Source == "adapter" {
			delete(d.agents.byName, name)
		}
		if !seen[name] && rec.Source == "hook" && now-rec.ReportedAt > int64(10*time.Minute) {
			delete(d.agents.byName, name)
		}
	}
}

// reportState records what a hook says about the calling agent. The
// principal is the connection's, never the request's.
func (d *Daemon) reportState(c *conn, a ReportStateArgs) (any, error) {
	who := c.pl.Principal
	if who == "" {
		return nil, errForbidden("a state report needs a placed caller (a pane or a token); this connection is outside every pane")
	}
	switch a.State {
	case adapter.Idle, adapter.Working, adapter.Blocked, adapter.Done, adapter.Unknown:
	default:
		return nil, errForbidden("state must be one of idle, working, blocked, done, unknown")
	}
	d.mu.Lock()
	rec := d.agents.byName[who]
	if rec == nil {
		rec = &agentRec{}
		d.agents.byName[who] = rec
	}
	rec.Principal = who
	rec.State = a.State
	if a.Harness != "" {
		rec.Harness = a.Harness
	}
	if a.Session != "" {
		rec.Session = a.Session
	}
	if a.Label != "" {
		rec.Label = a.Label
	}
	rec.ReportedAt, rec.Source = d.now().UnixNano(), "hook"
	snapshot := *rec
	d.mu.Unlock()
	d.publishAgent(snapshot)
	return snapshot, nil
}

func (d *Daemon) listAgents() AgentsResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]agentRec, 0, len(d.agents.byName))
	for _, r := range d.agents.byName {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Principal < out[j].Principal })
	return AgentsResult{Adapter: d.adapter.Name(), Agents: out}
}

// stateOf is the daemon's current view of an agent; Unknown when unseen.
func (d *Daemon) stateOf(p store.Address) (adapter.State, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.agents.byName[p]
	if !ok {
		return adapter.Unknown, false
	}
	return r.State, true
}
