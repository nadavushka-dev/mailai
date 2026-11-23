// Package harness is the glue between mailai and an agent harness (D14):
// declarative TOML manifests say where a harness keeps its hook config, how
// to add an entry, which of its events mean which of our five states, and
// whether a permission request can be held. Claude Code and Codex ship
// bundled; a user manifest in <config>/harnesses/ adds any other.
package harness

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/nadavushka-dev/mailai/internal/adapter"
)

//go:embed manifests/*.toml
var bundled embed.FS

// Manifest describes one harness.
type Manifest struct {
	// Name is the harness id used on the command line: claude-code, codex.
	Name string `toml:"name"`
	// Description is one line for `integration status`.
	Description string `toml:"description"`
	// Config is the hook config file, with ~ for the home directory.
	Config string `toml:"config"`
	// Format says how Config is edited: "claude-settings-json" (the hooks
	// object of Claude Code's settings.json) or "codex-hooks-json".
	Format string `toml:"format"`
	// Skills is the directory the harness reads Agent Skills from, with ~
	// for the home directory; integration install puts the guide there as
	// mailai/SKILL.md. Empty: no skill is installed for this harness.
	Skills string `toml:"skills"`
	// Events maps the harness's event name to what it means for mailai.
	Events map[string]Event `toml:"events"`
	// Approval names the event that is a permission request and how to
	// answer it; empty when the harness has none we can hold.
	Approval *Approval `toml:"approval"`
	// Notice says how a hook adds a line to the agent's context.
	Notice Notice `toml:"notice"`
	// Bundled is set on manifests shipped in the binary.
	Bundled bool `toml:"-"`
	// Path is where a user manifest was read from.
	Path string `toml:"-"`
}

// Event is one harness event.
type Event struct {
	// State is what the event means: idle, working, blocked, done, unknown,
	// or empty to report nothing.
	State adapter.State `toml:"state"`
	// Notice says the hook should add the mail notice line on this event.
	Notice bool `toml:"notice"`
	// Signal is what the event says beyond the state (SPEC §7a): "prompt"
	// (the agent got a new prompt), "tool_ran" (a tool finished; the
	// payload's tool_name names it) or "turn_ended". Needs a state.
	Signal string `toml:"signal"`
	// Matcher is the harness's tool matcher for this entry, when it has one.
	Matcher string `toml:"matcher"`
	// Timeout in seconds for the hook command, when the harness takes one.
	Timeout int `toml:"timeout"`
}

// Approval describes the permission-request event.
type Approval struct {
	Event string `toml:"event"`
	// Risky is a JSON pointer-like path in the payload whose truthiness
	// marks the request risky, as the harness says it; empty when the
	// harness says nothing (§8).
	Risky string `toml:"risky"`
	// Timeout in seconds the harness allows the hook to block.
	Timeout int `toml:"timeout"`
}

// Notice is how to deliver context text.
type Notice struct {
	// Stdout says plain stdout with exit 0 becomes context (per event list).
	StdoutEvents []string `toml:"stdout_events"`
	// JSONField, when set, means the line goes in
	// {"hookSpecificOutput":{"hookEventName":EVENT, <JSONField>: line}}.
	JSONField string `toml:"json_field"`
}

var ErrUnknown = errors.New("unknown harness")

// Load returns the manifests: bundled ones, then user ones from dir, which
// replace a bundled one of the same name.
func Load(userDir string) (map[string]*Manifest, error) {
	out := map[string]*Manifest{}
	ents, err := fs.ReadDir(bundled, "manifests")
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		b, err := bundled.ReadFile("manifests/" + e.Name())
		if err != nil {
			return nil, err
		}
		m, err := parse(b)
		if err != nil {
			return nil, fmt.Errorf("bundled %s: %w", e.Name(), err)
		}
		m.Bundled = true
		out[m.Name] = m
	}
	if userDir != "" {
		ents, err := os.ReadDir(userDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			p := filepath.Join(userDir, e.Name())
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			m, err := parse(b)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			m.Path = p
			out[m.Name] = m
		}
	}
	return out, nil
}

func parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Name == "" || m.Config == "" || m.Format == "" {
		return nil, errors.New("a manifest needs name, config and format")
	}
	for ev, e := range m.Events {
		switch e.State {
		case "", adapter.Idle, adapter.Working, adapter.Blocked, adapter.Done, adapter.Unknown:
		default:
			return nil, fmt.Errorf("event %s: state %q is not one of idle, working, blocked, done, unknown", ev, e.State)
		}
		switch e.Signal {
		case "", "prompt", "tool_ran", "turn_ended":
		default:
			return nil, fmt.Errorf("event %s: signal %q is not one of prompt, tool_ran, turn_ended", ev, e.Signal)
		}
		if e.Signal != "" && e.State == "" {
			return nil, fmt.Errorf("event %s: a signal needs a state", ev)
		}
	}
	return &m, nil
}

// Names lists manifests in order.
func Names(ms map[string]*Manifest) []string {
	out := make([]string, 0, len(ms))
	for n := range ms {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ConfigPath expands ~ in the manifest's config path.
func (m *Manifest) ConfigPath(home string) string { return expandHome(home, m.Config) }

func expandHome(home, p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Command is the hook command written into the harness config.
func (m *Manifest) Command(mailai, event string) string {
	return fmt.Sprintf("%s hook %s %s", mailai, m.Name, event)
}
