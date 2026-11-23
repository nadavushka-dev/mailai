package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nadavushka-dev/mailai/internal/adapter"
	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/config"
	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/harness"
	"github.com/nadavushka-dev/mailai/internal/store"
)

func loadManifests() (map[string]*harness.Manifest, error) {
	p, err := config.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return harness.Load(filepath.Join(p.Config, "harnesses"))
}

// cmdHook is what an installed hook runs: `mailai hook <harness> <event>`
// with the harness's payload on stdin (SPEC §8). It reports the mapped
// state, prints the notice line in the form the harness takes, and for the
// approval event holds the request until the Inbox decides. It must never
// fail the agent: any error exits 0 with nothing on stdout.
func cmdHook(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: mailai hook <harness> <event>  (payload on stdin)")
	}
	hname, event := args[0], args[1]
	ms, err := loadManifests()
	if err != nil {
		return err
	}
	m := ms[hname]
	if m == nil {
		return fmt.Errorf("%w: %s", harness.ErrUnknown, hname)
	}
	payload := map[string]any{}
	if b, _ := io.ReadAll(os.Stdin); len(b) > 0 {
		json.Unmarshal(b, &payload)
	}
	c, err := connect()
	if err != nil {
		// No daemon: the agent must not notice.
		return nil
	}
	defer c.Close()

	if m.Approval != nil && event == m.Approval.Event {
		return hookApproval(c, m, payload)
	}
	ev, known := m.Events[event]
	if !known {
		return nil
	}
	if ev.State != "" {
		label := ""
		if event == "Stop" {
			if s, _ := payload["last_assistant_message"].(string); s != "" {
				label, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
				if len(label) > 80 {
					label = label[:79] + "…"
				}
			}
		}
		// The signal (SPEC §7a) lets the daemon see the person answered in
		// the session; it adds nothing to the agent's context.
		tool := ""
		if ev.Signal == daemon.SignalToolRan {
			tool, _ = payload["tool_name"].(string)
		}
		c.Call("report-state", daemon.ReportStateArgs{State: adapter.State(ev.State), Harness: m.Name, Label: label, Signal: ev.Signal, Tool: tool}, nil)
	}
	if !ev.Notice {
		return nil
	}
	var nr daemon.NoticeResult
	if err := c.Call("notice", daemon.NoticeArgs{}, &nr); err != nil || !nr.New || nr.Line == "" {
		return nil
	}
	for _, e := range m.Notice.StdoutEvents {
		if e == event {
			fmt.Println(nr.Line)
			return nil
		}
	}
	if m.Notice.JSONField != "" {
		out := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, m.Notice.JSONField: nr.Line}}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	return nil
}

// hookApproval holds a permission request in the Inbox and prints the
// harness's decision object, or nothing when nobody decided in time so the
// harness falls back to its own prompt.
func hookApproval(c *client.Client, m *harness.Manifest, payload map[string]any) error {
	tool, _ := payload["tool_name"].(string)
	input, _ := payload["tool_input"].(map[string]any)
	summary := summarizeInput(tool, input)
	risky := false
	if m.Approval.Risky != "" {
		risky = truthy(payload[m.Approval.Risky])
	}
	raw, _ := json.Marshal(input)
	timeout := time.Duration(m.Approval.Timeout) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	// Leave the harness a margin before its own timeout.
	timeout -= 15 * time.Second
	var r daemon.HoldApprovalResult
	if err := c.Call("hold-approval", daemon.HoldApprovalArgs{Harness: m.Name, Tool: tool, Input: raw, Summary: summary, Risky: risky,
		TimeoutMS: timeout.Milliseconds()}, &r); err != nil || r.Decision == "" {
		return nil
	}
	dec := map[string]any{"behavior": r.Decision}
	if r.Decision == "deny" {
		msg := r.Message
		if msg == "" {
			msg = "denied from the mailai Inbox by " + r.By
		}
		dec["message"] = msg
	}
	out := map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": m.Approval.Event, "decision": dec}}
	return json.NewEncoder(os.Stdout).Encode(out)
}

// summarizeInput is one line of the tool input for the Inbox row: the
// command for a shell tool, the path for a file tool, else the first field.
func summarizeInput(tool string, in map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "url", "pattern", "query"} {
		if v, ok := in[k].(string); ok && v != "" {
			return firstLine(v)
		}
	}
	for k, v := range in {
		if s, ok := v.(string); ok {
			return firstLine(k + "=" + s)
		}
	}
	return tool
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "0"
	case float64:
		return x != 0
	}
	return v != nil
}

const integrationUsage = "usage: mailai integration install|uninstall|status [harness...] [--project DIR] [--skill-only] [--command PATH]"

// cmdIntegration installs, removes or checks, per manifest, the hook
// entries and the guide as an Agent Skill (SPEC §8). --project puts both in
// a repository instead of the home directory; --skill-only leaves hooks be.
func cmdIntegration(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(integrationUsage)
	}
	sub := args[0]
	switch sub {
	case "install", "uninstall", "status":
	default:
		return fmt.Errorf(integrationUsage)
	}
	fs := newFlags("integration " + sub)
	cmdPath := fs.String("command", "", "path of the mailai binary to write into hooks; this binary by default")
	project := fs.String("project", "", "use this repository's DIR/.claude and DIR/.agents instead of the home directory's")
	skillOnly := fs.Bool("skill-only", false, "only the skill (the guide), not the hooks")
	names := parse(fs, args[1:])
	ms, err := loadManifests()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = harness.Names(ms)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if h := os.Getenv("MAILAI_TEST_HOME"); h != "" {
		home = h
	}
	if *project != "" {
		dir, err := filepath.Abs(*project)
		if err != nil {
			return err
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("--project %s: not a directory", *project)
		}
		// A repository mirrors the home's layout: DIR/.claude, DIR/.agents.
		home = dir
	}
	// Under --project only paths below ~ move into the repository; an
	// absolute path in a user manifest would reach outside it.
	inScope := func(p string) bool { return *project == "" || strings.HasPrefix(p, "~/") }
	bin := *cmdPath
	if bin == "" {
		bin, _ = os.Executable()
		if p, err := exec.LookPath("mailai"); err == nil && filepath.Base(bin) == "mailai" {
			bin = p
		}
	}
	var out []harness.Status
	for _, n := range names {
		m := ms[n]
		if m == nil {
			return fmt.Errorf("%w: %s", harness.ErrUnknown, n)
		}
		hooks := !*skillOnly
		if hooks && !inScope(m.Config) {
			hooks = false
			if sub != "status" && !jsonOut {
				fmt.Printf("%s: hook config %s is not per-project; hooks left alone\n", n, m.Config)
			}
		}
		skill := ""
		if inScope(m.Skills) {
			skill = m.SkillPath(home)
		}
		switch sub {
		case "install":
			if hooks {
				changed, err := m.Install(home, bin)
				var me *harness.ManualError
				switch {
				case err != nil && asManual(err, &me):
					fmt.Fprintln(os.Stderr, err)
				case err != nil:
					return err
				case jsonOut:
				case changed:
					fmt.Printf("%s: hooks written to %s\n", n, m.ConfigPath(home))
				default:
					fmt.Printf("%s: already current\n", n)
				}
			}
			if skill != "" {
				r, err := harness.InstallSkill(skill)
				if err != nil {
					return err
				}
				printSkillResult(n, skill, r)
			}
		case "uninstall":
			if hooks {
				changed, err := m.Uninstall(home)
				if err != nil {
					return err
				}
				if !jsonOut {
					if changed {
						fmt.Printf("%s: hooks removed from %s\n", n, m.ConfigPath(home))
					} else {
						fmt.Printf("%s: nothing to remove\n", n)
					}
				}
			}
			if skill != "" {
				r, err := harness.UninstallSkill(skill)
				if err != nil {
					return err
				}
				printSkillResult(n, skill, r)
			}
		case "status":
			st := harness.Status{Name: n}
			if hooks {
				st = m.Check(home, bin)
			}
			if skill != "" {
				st.Skill, st.SkillState = skill, harness.CheckSkill(skill)
			}
			out = append(out, st)
		}
	}
	if sub == "status" {
		if jsonOut {
			return printJSON(out)
		}
		for _, st := range out {
			if st.Config != "" {
				state := "not installed"
				switch {
				case st.Installed && st.Current:
					state = "installed, current"
				case st.Installed:
					state = fmt.Sprintf("installed, not current (missing %v, stale %v)", st.Missing, st.Stale)
				}
				fmt.Printf("%-12s %-50s %s\n", st.Name, st.Config, state)
			}
			if st.Skill != "" {
				state := "not installed"
				switch st.SkillState {
				case harness.SkillCurrent:
					state = "installed, current"
				case harness.SkillStale:
					state = "installed, stale (another mailai wrote it; install rewrites it)"
				case harness.SkillForeign:
					state = "not written by mailai; left alone"
				}
				fmt.Printf("%-12s %-50s skill %s\n", st.Name, st.Skill, state)
			}
		}
	}
	return nil
}

func printSkillResult(name, path string, r harness.SkillState) {
	if jsonOut {
		return
	}
	switch r {
	case harness.SkillWritten:
		fmt.Printf("%s: skill written to %s\n", name, path)
	case harness.SkillUpdated:
		fmt.Printf("%s: skill updated at %s\n", name, path)
	case harness.SkillCurrent:
		fmt.Printf("%s: skill already current at %s\n", name, path)
	case harness.SkillRemoved:
		fmt.Printf("%s: skill removed from %s\n", name, path)
	case harness.SkillAbsent:
		fmt.Printf("%s: no skill to remove at %s\n", name, path)
	case harness.SkillForeign:
		fmt.Printf("%s: %s was not written by mailai; left alone\n", name, path)
	}
}

func asManual(err error, target **harness.ManualError) bool {
	me, ok := err.(*harness.ManualError)
	if ok {
		*target = me
	}
	return ok
}

// cmdAddon writes a harness's add-on (SPEC §7): `mailai addon pi [DIR]`.
func cmdAddon(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		return fmt.Errorf("usage: mailai addon <harness> [DIR]; harnesses: %v", harness.AddonHarnesses())
	}
	h := args[0]
	dir := ""
	if len(args) > 1 {
		dir = args[1]
	} else {
		p, err := config.DefaultPaths()
		if err != nil {
			return err
		}
		dir = filepath.Join(p.Data, "addons", h)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	sk, err := harness.WriteAddon(h, abs)
	if err != nil {
		return err
	}
	fmt.Printf("%s add-on written to %s (skill %s)\nload it into one session: pi -e %s\n", h, abs, sk, abs)
	return nil
}

func cmdSkill(args []string) error {
	fmt.Print(harness.Skill)
	return nil
}

var _ = store.Human
