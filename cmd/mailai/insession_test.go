package main

import (
	"strings"
	"testing"
	"time"
)

// SPEC §7a end to end: the person answers the harness's own permission
// dialog, the tool runs, and PostToolUse lets the held hook go with no
// decision; the Inbox lists it as closed in session.
func TestCLI_HookToolRanReleasesHeldApproval(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "worker"), "\n", 2)[0])
	hook := func(event, payload string) (string, error) {
		c := e.cmd("hook", "claude-code", event)
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		c.Stdin = strings.NewReader(payload)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	type res struct {
		out string
		err error
	}
	got := make(chan res, 1)
	go func() {
		out, err := hook("PermissionRequest", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"git push"}}`)
		got <- res{out, err}
	}()
	var out string
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out, "approval") && time.Now().Before(deadline) {
		out = e.must("inbox")
		time.Sleep(20 * time.Millisecond)
	}
	if out, err := hook("PostToolUse", `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`); err != nil || out != "" {
		t.Fatalf("%q %v", out, err)
	}
	select {
	case r := <-got:
		if r.err != nil || r.out != "" {
			t.Fatalf("held hook must print no decision: %q %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("held hook never released")
	}
	if out := e.must("inbox"); !strings.HasPrefix(out, "inbox empty") {
		t.Fatal(out)
	}
	if out := e.must("inbox", "--closed"); !strings.Contains(out, "#1  approval") || !strings.Contains(out, "(in session") {
		t.Fatalf("%q", out)
	}
}

// A new prompt while an ask-human waits: the item is badged, still open,
// and the JSON carries the field; recently closed says how each closed.
func TestCLI_InboxMaybeBadgeAndRecentlyClosed(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "worker"), "\n", 2)[0])
	agent := func(args ...string) string {
		c := e.cmd(args...)
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return string(out)
	}
	agent("ask-human", "Region?", "-o", "us", "-o", "eu", "--no-wait")
	agent("ask-human", "Proceed?", "--no-wait")
	c := e.cmd("hook", "claude-code", "UserPromptSubmit")
	c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
	c.Stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"use eu"}`)
	if out, err := c.CombinedOutput(); err != nil || strings.Contains(string(out), "session") {
		t.Fatalf("%q %v", out, err)
	}
	out := e.must("inbox")
	if strings.Count(out, "maybe answered in session") != 2 {
		t.Fatalf("%q", out)
	}
	if out := e.must("inbox", "--json"); !strings.Contains(out, `"maybe_in_session_at"`) {
		t.Fatal(out)
	}
	e.must("answer", "1", "eu")
	e.must("dismiss", "2")
	out = e.must("inbox", "--closed")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#2") || !strings.Contains(lines[0], "(dismissed") ||
		!strings.HasPrefix(lines[1], "#1") || !strings.Contains(lines[1], "(by you") {
		t.Fatalf("%q", out)
	}
	if out := e.must("inbox", "--closed", "--json"); !strings.Contains(out, `"closed_by": "dismissed"`) || !strings.Contains(out, `"decided_by": "human"`) {
		t.Fatal(out)
	}
}
