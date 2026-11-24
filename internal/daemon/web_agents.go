package daemon

import (
	"net/http"
	"sort"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// AgentSeen is one agent the daemon has seen live since it started, and
// when it last did (SPEC §12a).
type AgentSeen struct {
	Principal store.Address `json:"principal"`
	Name      string        `json:"name,omitempty"`
	PaneID    string        `json:"pane_id,omitempty"`
	Live      bool          `json:"live"`
	// LastSeen is when it was last listed live (ns); now for a live one.
	LastSeen int64 `json:"last_seen"`
}

// AgentsSeenResult is what the web Agents list marks liveness from.
type AgentsSeenResult struct {
	// Known is false when no adapter tells who is live: then nothing is
	// closed, only unknown.
	Known bool `json:"known"`
	// Machine is this machine's name: name@machine is local.
	Machine string      `json:"machine,omitempty"`
	Agents  []AgentSeen `json:"agents"`
	// Unknown are names whose liveness cannot be known: token-held names
	// and mailai's own bounces.
	Unknown []store.Address `json:"unknown"`
}

func (d *Daemon) agentsSeen() AgentsSeenResult {
	_, none := d.adapter.(adapter.None)
	res := AgentsSeenResult{Known: !none, Machine: d.name, Agents: []AgentSeen{}, Unknown: []store.Address{Bouncer}}
	d.mu.Lock()
	for name, s := range d.agents.seen {
		a := *s
		_, a.Live = d.agents.byName[name]
		res.Agents = append(res.Agents, a)
	}
	d.mu.Unlock()
	sort.Slice(res.Agents, func(i, j int) bool { return res.Agents[i].Principal < res.Agents[j].Principal })
	if d.tokens != nil {
		for _, r := range d.tokens.List() {
			if !containsAddr(res.Unknown, r.Name) {
				res.Unknown = append(res.Unknown, r.Name)
			}
		}
	}
	return res
}

func containsAddr(as []store.Address, a store.Address) bool {
	for _, x := range as {
		if x == a {
			return true
		}
	}
	return false
}

// apiAgents is the person's view of who is live, for the Agents list.
func (d *Daemon) apiAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, d.agentsSeen())
}
