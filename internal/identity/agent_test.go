package identity

import (
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// fakeArgs is argv per pid.
type fakeArgs map[int][]string

func (f fakeArgs) Args(pid int) ([]string, bool) {
	a, ok := f[pid]
	return a, ok
}

// agentPanes is the multiplexer's word on which panes run an agent.
type agentPanes struct {
	fakePanes
	agents map[store.Address]bool
}

func (a agentPanes) RunsAgent(pane store.Address) bool { return a.agents[pane] }

type ttys map[int]bool

func (t ttys) HasTTY(pid int) bool { return t[pid] }

// Decided 2026-11-22 (SPEC §9): who is an agent is decided by "is an agent
// program running it", not "is it in a Herdr pane". Tree:
//
//	1 -> 100 herdr -> 200 shell w1:p3 (herdr: agent)  -> 300 claude -> 400 mailai
//	               -> 220 shell w1:p5 (herdr: none)   -> 320 mailai      the person in Herdr
//	               -> 230 shell w1:p6 (herdr: not yet) -> 330 claude -> 430 bash -> 530 mailai
//	1 -> 500 terminal -> 600 zsh -> 700 mailai                           the person
//	                             -> 610 claude -> 620 bash -> 630 mailai an agent outside Herdr
//	                             -> 640 node .../claude-code/cli.js -> 650 mailai
//	1 -> 800 claude (started by herdr's parent?) -> 810 herdr2 -> 820 shell w9:p1 -> 830 mailai
var agentTree = map[int]int{100: 1, 200: 100, 300: 200, 400: 300, 220: 100, 320: 220, 230: 100, 330: 230, 430: 330, 530: 430,
	500: 1, 600: 500, 700: 600, 610: 600, 620: 610, 630: 620, 640: 600, 650: 640,
	800: 1, 810: 800, 820: 810, 830: 820}

var agentArgs = fakeArgs{
	100: {"herdr", "server"}, 200: {"-zsh"}, 300: {"claude"}, 400: {"mailai", "read"},
	220: {"-zsh"}, 320: {"mailai", "tui"}, 230: {"-bash"}, 330: {"/home/u/.local/bin/claude", "--resume"}, 430: {"bash", "-c", "mailai unread"}, 530: {"mailai"},
	500: {"/usr/bin/gnome-terminal"}, 600: {"zsh"}, 700: {"mailai"}, 610: {"claude"}, 620: {"bash"}, 630: {"mailai", "answer", "1", "yes"},
	640: {"node", "--no-warnings", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, 650: {"mailai"},
	800: {"claude"}, 810: {"herdr", "server"}, 820: {"zsh"}, 830: {"mailai", "tui"},
}

func agentPlacer(peer int) *Placer {
	everyoneHasTTY := ttys{}
	for pid := range agentTree {
		everyoneHasTTY[pid] = true
	}
	return &Placer{
		Proc:   fakeProc{parent: agentTree, peer: peer},
		Panes:  agentPanes{fakePanes{200: "w1:p3", 220: "w1:p5", 230: "w1:p6", 820: "w9:p1"}, map[store.Address]bool{"w1:p3": true}},
		Tokens: fakeTokens{"mt_ok": "helper"},
		TTY:    everyoneHasTTY,
		Args:   agentArgs,
	}
}

func TestAgentOrPerson_InsideHerdr(t *testing.T) {
	cases := []struct {
		name  string
		peer  int
		token string
		proof store.Proof
		who   store.Address
	}{
		{"an agent pane is that agent", 400, "", store.ProofPane, "w1:p3"},
		{"so is its shell, herdr says an agent runs there", 200, "", store.ProofPane, "w1:p3"},
		{"a plain shell pane is the person", 320, "", store.ProofOutsidePane, ""},
		{"an agent herdr has not noticed yet is caught by its parents", 530, "", store.ProofPane, "w1:p6"},
		{"a token in a shell pane is that agent", 320, "mt_ok", store.ProofToken, "helper"},
		// Only the pane's own processes count: what started herdr does not
		// make every pane an agent.
		{"herdr started under an agent: the shell pane is still the person", 830, "", store.ProofOutsidePane, ""},
	}
	for _, c := range cases {
		pl := agentPlacer(c.peer).Place(nil, c.token)
		if pl.Proof != c.proof || pl.Principal != c.who {
			t.Errorf("%s: got %+v, want %s %q", c.name, pl, c.proof, c.who)
		}
	}
}

func TestAgentOrPerson_OutsideHerdr(t *testing.T) {
	cases := []struct {
		name  string
		peer  int
		token string
		proof store.Proof
	}{
		{"a terminal is the person", 700, "", store.ProofOutsidePane},
		{"claude among the parents is an agent, never the person", 630, "", store.ProofAgent},
		{"also when it runs under node", 650, "", store.ProofAgent},
		{"an agent with a token is that agent", 630, "mt_ok", store.ProofToken},
	}
	for _, c := range cases {
		if pl := agentPlacer(c.peer).Place(nil, c.token); pl.Proof != c.proof {
			t.Errorf("%s: got %+v, want %s", c.name, pl, c.proof)
		}
	}
	if pl := agentPlacer(630).Place(nil, ""); pl.Agent != "claude" {
		t.Errorf("the placement should name the program, for the error message: %+v", pl)
	}
}

// Without a terminal a shell-pane caller is not the person either (a
// daemonized child left in the pane's process group, say).
func TestAgentOrPerson_ShellPaneWithoutTerminalIsDetached(t *testing.T) {
	p := agentPlacer(320)
	p.TTY = ttys{}
	if pl := p.Place(nil, ""); pl.Proof != store.ProofDetached {
		t.Fatalf("%+v", pl)
	}
}

func TestIsAgentProgram(t *testing.T) {
	yes := [][]string{
		{"claude"}, {"/home/u/.local/bin/claude", "-p"}, {"pi"}, {"pi", "", "", ""},
		{"/home/u/.codex/packages/app-server-daemon/releases/0.171.0/bin/codex", "app-server"},
		{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"},
		{"/usr/bin/node", "--max-old-space-size=4096", "/opt/gemini/bin/gemini"},
		{"opencode"}, {"aider"}, {"goose", "session"}, {"cursor-agent"},
	}
	no := [][]string{
		{"zsh"}, {"-bash"}, {"mailai", "tui"}, {"node", "server.js"}, {"python3", "-m", "http.server"},
		{"vim", "claude.md"}, {"herdr", "server"}, {"pip"}, {"pipewire"}, {},
	}
	for _, a := range yes {
		if _, ok := IsAgentProgram(a); !ok {
			t.Errorf("%q should be an agent program", a)
		}
	}
	for _, a := range no {
		if name, ok := IsAgentProgram(a); ok {
			t.Errorf("%q is not an agent program (matched %q)", a, name)
		}
	}
}
