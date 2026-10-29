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
		c.Call("report-state", daemon.ReportStateArgs{State: adapter.State(ev.State), Harness: m.Name, Label: label}, nil)
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

// cmdIntegration installs, removes or checks hook entries per manifest.
func cmdIntegration(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mailai integration install|uninstall|status [harness...]")
	}
	sub := args[0]
	fs := newFlags("integration " + sub)
	cmdPath := fs.String("command", "", "path of the mailai binary to write into hooks; this binary by default")
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
		switch sub {
		case "install":
			changed, err := m.Install(home, bin)
			if err != nil {
				var me *harness.ManualError
				if ok := asManual(err, &me); ok {
					fmt.Fprintln(os.Stderr, err)
					continue
				}
				return err
			}
			if !jsonOut {
				if changed {
					fmt.Printf("%s: hooks written to %s\n", n, m.ConfigPath(home))
				} else {
					fmt.Printf("%s: already current\n", n)
				}
			}
		case "uninstall":
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
		case "status":
			out = append(out, m.Check(home, bin))
		default:
			return fmt.Errorf("usage: mailai integration install|uninstall|status [harness...]")
		}
	}
	if sub == "status" {
		if jsonOut {
			return printJSON(out)
		}
		for _, st := range out {
			state := "not installed"
			switch {
			case st.Installed && st.Current:
				state = "installed, current"
			case st.Installed:
				state = fmt.Sprintf("installed, not current (missing %v, stale %v)", st.Missing, st.Stale)
			}
			fmt.Printf("%-12s %-50s %s\n", st.Name, st.Config, state)
		}
	}
	return nil
}

func asManual(err error, target **harness.ManualError) bool {
	me, ok := err.(*harness.ManualError)
	if ok {
		*target = me
	}
	return ok
}

func cmdSkill(args []string) error {
	fmt.Print(harness.Skill)
	return nil
}

var _ = store.Human
