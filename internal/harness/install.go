package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Install writes mailai's hook entries into a harness config, leaving every
// other entry alone. Entries are recognised by their command, which starts
// with "<mailai> hook <harness> ", so uninstall removes exactly what install
// wrote and nothing else. Only the "claude-settings-json" format exists so
// far: a top-level "hooks" object of event -> [{matcher, hooks: [{type,
// command, timeout}]}], which Claude Code uses and Codex is believed to.

// ManualError says a harness has no editable config we know; it carries
// the commands to wire by hand.
type ManualError struct {
	Manifest *Manifest
	Mailai   string
}

func (e *ManualError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: hook config cannot be edited automatically; add these commands to %s:\n", e.Manifest.Name, e.Manifest.Config)
	evs := make([]string, 0, len(e.Manifest.Events))
	for ev := range e.Manifest.Events {
		evs = append(evs, ev)
	}
	sort.Strings(evs)
	for _, ev := range evs {
		fmt.Fprintf(&b, "  %s -> %s\n", ev, e.Manifest.Command(e.Mailai, ev))
	}
	return strings.TrimRight(b.String(), "\n")
}

// Status of one harness's integration.
type Status struct {
	Name      string `json:"name"`
	Config    string `json:"config"`
	Installed bool   `json:"installed"`
	// Current says every entry the manifest wants is present with the
	// expected command; false when partially installed or stale.
	Current bool     `json:"current"`
	Missing []string `json:"missing,omitempty"`
	Stale   []string `json:"stale,omitempty"`
	// Skill is where the guide is installed as an Agent Skill, and
	// SkillState what is there; empty when the manifest names no skills
	// directory.
	Skill      string     `json:"skill,omitempty"`
	SkillState SkillState `json:"skill_state,omitempty"`
}

type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type hookGroup struct {
	Matcher string      `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

func (m *Manifest) wanted(mailai string) map[string]hookGroup {
	want := map[string]hookGroup{}
	for ev, e := range m.Events {
		want[ev] = hookGroup{Matcher: e.Matcher, Hooks: []hookEntry{{Type: "command", Command: m.Command(mailai, ev), Timeout: e.Timeout}}}
	}
	if m.Approval != nil && m.Approval.Event != "" {
		want[m.Approval.Event] = hookGroup{Hooks: []hookEntry{{Type: "command", Command: m.Command(mailai, m.Approval.Event), Timeout: m.Approval.Timeout}}}
	}
	return want
}

func (m *Manifest) prefix(mailai string) string { return mailai + " hook " + m.Name + " " }

// isOurs says whether a command is one mailai wrote for this harness, by
// any mailai path (so a moved binary still counts as ours, but stale).
func (m *Manifest) isOurs(cmd string) bool {
	i := strings.Index(cmd, " hook "+m.Name+" ")
	if i < 0 {
		return false
	}
	bin := cmd[:i]
	return strings.HasSuffix(bin, "mailai") || strings.HasSuffix(bin, "mailai.exe")
}

// Install adds or refreshes the entries; it reports what changed.
func (m *Manifest) Install(home, mailai string) (changed bool, err error) {
	if m.Format == "manual" {
		return false, &ManualError{Manifest: m, Mailai: mailai}
	}
	if m.Format != "claude-settings-json" {
		return false, fmt.Errorf("manifest %s: format %q is not supported", m.Name, m.Format)
	}
	path := m.ConfigPath(home)
	root, err := readJSON(path)
	if err != nil {
		return false, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	before := canon(hooks)
	for ev, group := range m.wanted(mailai) {
		groups := toGroups(hooks[ev])
		kept := groups[:0]
		for _, g := range groups {
			g.Hooks = filterOut(g.Hooks, m.isOurs)
			if len(g.Hooks) > 0 {
				kept = append(kept, g)
			}
		}
		kept = append(kept, group)
		hooks[ev] = kept
	}
	root["hooks"] = hooks
	if before == canon(hooks) {
		return false, nil
	}
	return true, writeJSON(path, root)
}

// canon renders a hooks object in one field order, so a no-op install is
// detected whatever shape the values arrived in.
func canon(hooks map[string]any) string {
	c := map[string][]hookGroup{}
	for ev, v := range hooks {
		c[ev] = toGroups(v)
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// Uninstall removes exactly the entries mailai wrote.
func (m *Manifest) Uninstall(home string) (changed bool, err error) {
	path := m.ConfigPath(home)
	root, err := readJSON(path)
	if err != nil {
		return false, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	for ev := range hooks {
		groups := toGroups(hooks[ev])
		kept := groups[:0]
		for _, g := range groups {
			n := len(g.Hooks)
			g.Hooks = filterOut(g.Hooks, m.isOurs)
			if len(g.Hooks) > 0 || (n == 0) {
				kept = append(kept, g)
			} else if len(g.Hooks) == 0 && n > 0 {
				changed = true
			}
			if len(g.Hooks) != n {
				changed = true
			}
		}
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if !changed {
		return false, nil
	}
	root["hooks"] = hooks
	return true, writeJSON(path, root)
}

// Check reports the state of the integration.
func (m *Manifest) Check(home, mailai string) Status {
	st := Status{Name: m.Name, Config: m.ConfigPath(home)}
	root, err := readJSON(st.Config)
	if err != nil {
		return st
	}
	hooks, _ := root["hooks"].(map[string]any)
	present := map[string]string{} // event -> command found
	for ev := range hooks {
		for _, g := range toGroups(hooks[ev]) {
			for _, h := range g.Hooks {
				if m.isOurs(h.Command) {
					present[ev] = h.Command
				}
			}
		}
	}
	st.Installed = len(present) > 0
	st.Current = st.Installed
	for ev, g := range m.wanted(mailai) {
		cmd, ok := present[ev]
		switch {
		case !ok:
			st.Missing = append(st.Missing, ev)
			st.Current = false
		case cmd != g.Hooks[0].Command:
			st.Stale = append(st.Stale, ev)
			st.Current = false
		}
	}
	sort.Strings(st.Missing)
	sort.Strings(st.Stale)
	return st
}

func filterOut(hs []hookEntry, ours func(string) bool) []hookEntry {
	out := hs[:0]
	for _, h := range hs {
		if !ours(h.Command) {
			out = append(out, h)
		}
	}
	return out
}

func toGroups(v any) []hookGroup {
	b, _ := json.Marshal(v)
	var gs []hookGroup
	json.Unmarshal(b, &gs)
	return gs
}

func readJSON(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	root := map[string]any{}
	if len(strings.TrimSpace(string(b))) == 0 {
		return root, nil
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return root, nil
}

func writeJSON(path string, root map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".mailai-tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
