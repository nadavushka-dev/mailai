package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nadavushka-dev/mailai/internal/procinfo"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Runner runs the herdr CLI; a fake stands in for it in tests.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner runs the real binary.
type ExecRunner struct{ Bin string }

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "herdr"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("herdr %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("herdr %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// Herdr reads agents and panes from the herdr CLI and places processes by
// the pane shell's recorded environment (SPEC §9, Q4): the shell of a pane
// is a direct child of the herdr server, and the kernel keeps the
// environment herdr started it with, so HERDR_PANE_ID read there is herdr's
// word, not the caller's.
type Herdr struct {
	Run  Runner
	Proc procinfo.Reader
	// Socket is herdr's unix socket; the server's pid is the peer of a
	// connection to it. HERDR_SOCKET_PATH when empty.
	Socket string
	// Dial connects to the socket; net.Dial when nil.
	Dial func(path string) (net.Conn, error)

	mu        sync.Mutex
	serverPID int
	serverAt  time.Time
	names     map[string]store.Address // pane id -> principal, from the last Agents

	// taken says a name belongs to a mailai token (SPEC §9); logf tells the
	// daemon's log. warned is the pane+name pairs already logged, so a
	// poll does not repeat it.
	taken  func(store.Address) bool
	logf   func(format string, args ...any)
	warned map[string]bool
}

// Reserve tells the adapter which names mailai tokens hold: a Herdr agent
// with such a name is placed under its pane id, never the token's name.
func (h *Herdr) Reserve(taken func(store.Address) bool, logf func(format string, args ...any)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.taken, h.logf = taken, logf
}

func (h *Herdr) Name() string { return "herdr" }

// herdrAgent is one row of `herdr agent list`.
type herdrAgent struct {
	Name          string `json:"name"`
	Agent         string `json:"agent"`
	AgentStatus   string `json:"agent_status"`
	PaneID        string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	TitleStripped string `json:"terminal_title_stripped"`
}

// Agents runs `herdr agent list`. The principal is the live agent name when
// one is set and no token holds it, else the pane id; the session is the
// workspace.
func (h *Herdr) Agents(ctx context.Context) ([]Agent, error) {
	out, err := h.Run.Run(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	var env struct {
		Result struct {
			Agents []herdrAgent `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("herdr agent list: %w", err)
	}
	taken, logf := h.reserved()
	names := map[string]store.Address{}
	warn := map[string]string{} // pane+name -> the line saying why
	agents := make([]Agent, 0, len(env.Result.Agents))
	for _, a := range env.Result.Agents {
		p := principalOf(a)
		if a.Name != "" && taken != nil && taken(store.Address(a.Name)) {
			p = store.Address(a.PaneID)
			warn[a.PaneID+"\x00"+a.Name] = fmt.Sprintf("herdr agent %q in pane %s gets mailbox %s: %q is a mailai token's name",
				a.Name, a.PaneID, a.PaneID, a.Name)
		}
		names[a.PaneID] = p
		agents = append(agents, Agent{Principal: p, Name: a.Name, Label: a.TitleStripped, Session: a.WorkspaceID,
			State: mapState(a.AgentStatus), Harness: a.Agent, PaneID: a.PaneID})
	}
	var lines []string
	h.mu.Lock()
	h.names = names
	for key, line := range warn {
		if !h.warned[key] {
			lines = append(lines, line)
		}
	}
	// Only what is still the case stays warned, so it is said again if it
	// happens again later.
	h.warned = map[string]bool{}
	for key := range warn {
		h.warned[key] = true
	}
	h.mu.Unlock()
	if logf != nil {
		sort.Strings(lines)
		for _, l := range lines {
			logf("%s", l)
		}
	}
	return agents, nil
}

func (h *Herdr) reserved() (func(store.Address) bool, func(string, ...any)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.taken, h.logf
}

func principalOf(a herdrAgent) store.Address {
	if a.Name != "" {
		return store.Address(a.Name)
	}
	return store.Address(a.PaneID)
}

func mapState(s string) State {
	switch s {
	case "idle":
		return Idle
	case "working":
		return Working
	case "blocked":
		return Blocked
	case "done":
		return Done
	}
	return Unknown
}

// Type checks the agent is at rest right now, then runs `herdr agent
// prompt`, which sends the line and Enter as one submission. It never types
// at a blocked or working agent (SPEC §7).
func (h *Herdr) Type(ctx context.Context, principal store.Address, line string) error {
	out, err := h.Run.Run(ctx, "agent", "get", string(principal))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoAgent, err)
	}
	var env struct {
		Result struct {
			Agent herdrAgent `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return err
	}
	if !mapState(env.Result.Agent.AgentStatus).AtRest() {
		return fmt.Errorf("%w: %s is %s", ErrNotAtRest, principal, env.Result.Agent.AgentStatus)
	}
	_, err = h.Run.Run(ctx, "agent", "prompt", string(principal), line)
	return err
}

// PaneOfPID: pid is a pane's shell when its parent is the herdr server; the
// pane is the shell's recorded HERDR_PANE_ID. Identity walks the caller's
// ancestry and asks about each pid, so the shell is reached from any
// process inside the pane.
func (h *Herdr) PaneOfPID(pid int) (store.Address, bool) {
	id, ok := h.PaneIDOfPID(pid)
	if !ok {
		return "", false
	}
	h.mu.Lock()
	p, named := h.names[id]
	taken := h.taken
	h.mu.Unlock()
	// A token issued since the last poll already holds the name.
	if named && p != store.Address(id) && taken != nil && taken(p) {
		return store.Address(id), true
	}
	if named {
		return p, true
	}
	return store.Address(id), true
}

// PaneIDOfPID is the pane id of a pane's shell, whatever its principal is:
// shown beside an agent's name so a name that moved panes is visible (§9).
func (h *Herdr) PaneIDOfPID(pid int) (string, bool) {
	if h.Proc == nil {
		return "", false
	}
	server := h.server()
	if server == 0 {
		return "", false
	}
	parent, ok := h.Proc.ParentPID(pid)
	if !ok || parent != server {
		return "", false
	}
	id, ok := h.Proc.EnvVar(pid, "HERDR_PANE_ID")
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// RunsAgent says whether herdr lists an agent in a pane (by the principal
// PaneOfPID gave). A pane not in the last list is checked afresh, since an
// agent may have started since; when herdr cannot say, the pane counts as
// an agent's, never the person's (SPEC §9, decided 2026-11-22).
func (h *Herdr) RunsAgent(pane store.Address) bool {
	if h.listed(pane) {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := h.Agents(ctx); err != nil {
		return true
	}
	return h.listed(pane)
}

func (h *Herdr) listed(pane store.Address) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, p := range h.names {
		if p == pane || store.Address(id) == pane {
			return true
		}
	}
	return false
}

// server is the herdr server's pid: the peer of a connection to its socket,
// cached and re-checked when the process is gone.
func (h *Herdr) server() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.serverPID != 0 {
		if _, alive := h.Proc.ParentPID(h.serverPID); alive && time.Since(h.serverAt) < time.Minute {
			return h.serverPID
		}
	}
	sock := h.Socket
	if sock == "" {
		sock = os.Getenv("HERDR_SOCKET_PATH")
	}
	if sock == "" {
		return 0
	}
	dial := h.Dial
	if dial == nil {
		dial = func(p string) (net.Conn, error) { return net.DialTimeout("unix", p, time.Second) }
	}
	c, err := dial(sock)
	if err != nil {
		return 0
	}
	defer c.Close()
	h.serverPID = h.Proc.PeerPID(c)
	h.serverAt = time.Now()
	return h.serverPID
}
