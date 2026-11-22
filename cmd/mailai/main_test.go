package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The CLI is tested as a process: the binary is built once, a daemon runs
// against a throwaway MAILAI_HOME, and commands are run the way a hook or
// an agent would run them.

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mailai-bin")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "mailai")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t    *testing.T
	home string
	sock string
}

func startEnv(t *testing.T) *env {
	t.Helper()
	b := make([]byte, 4)
	rand.Read(b)
	id := hex.EncodeToString(b)
	home := t.TempDir()
	sock := filepath.Join(os.TempDir(), "mailai-cli-"+id+".sock")
	e := &env{t: t, home: home, sock: sock}
	// Test processes have no terminal; the person check is covered by the
	// identity and daemon tests. Tests never pop desktop notifications.
	os.MkdirAll(filepath.Join(home, "config"), 0o700)
	os.WriteFile(filepath.Join(home, "config", "config.toml"), []byte("[identity]\nrequire_terminal = false\n[notify]\nenabled = false\n"), 0o600)
	cmd := e.cmd("daemon", "run")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
		os.Remove(sock)
	})
	// Generous: under a race-detector test load the daemon can take a while.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			if _, err := e.run("ping"); err == nil {
				return e
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not come up")
	return nil
}

func (e *env) cmd(args ...string) *exec.Cmd {
	c := exec.Command(bin, args...)
	c.Env = append(os.Environ(), "MAILAI_HOME="+e.home, "MAILAI_SOCKET="+e.sock, "MAILAI_ADAPTER=none")
	return c
}

func (e *env) run(args ...string) (string, error) {
	out, err := e.cmd(args...).CombinedOutput()
	return string(out), err
}

func (e *env) must(args ...string) string {
	e.t.Helper()
	out, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("mailai %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestCLI_SendReadNoticeFence(t *testing.T) {
	e := startEnv(t)
	out := e.must("whoami")
	if !strings.Contains(out, "human (outside_pane)") {
		t.Fatal(out)
	}
	e.must("send", "worker", "--subject", "task", "fix the retry loop")
	c := e.cmd("send", "--from", "worker", "human")
	c.Stdin = strings.NewReader("on it\nsecond line\n")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	if out := e.must("unread"); !strings.HasPrefix(out, "1 unread from worker") {
		t.Fatal(out)
	}
	if out := e.must("notice"); strings.TrimSpace(out) != "You have 1 unread mailai message from worker. Read it with: mailai read --unread" {
		t.Fatal(out)
	}
	if out := e.must("read", "--summary"); !strings.HasPrefix(out, "* #2 worker (outside_pane) (no subject)") {
		t.Fatal(out)
	}
	out = e.must("read")
	want := "── #2 from worker (outside_pane) ──\n│ on it\n│ second line\n── end ──\n"
	if out != want {
		t.Fatalf("got %q", out)
	}
	if out := e.must("unread"); !strings.HasPrefix(out, "0 unread") {
		t.Fatal(out)
	}
	if out := e.must("notice"); out != "" {
		t.Fatalf("no mail must print nothing, got %q", out)
	}
	// --json everywhere.
	var msgs struct{ Messages []map[string]any }
	if err := json.Unmarshal([]byte(e.must("read", "-w", "worker", "--json")), &msgs); err != nil || len(msgs.Messages) != 1 || msgs.Messages[0]["subject"] != "task" {
		t.Fatal(err, msgs)
	}
}

// SPEC §7: ask blocks in the process and prints the reply alone; a timeout
// exits 2 so a script can tell "no" from "no answer".
func TestCLI_AskPrintsBareReplyAndTimesOutWithExit2(t *testing.T) {
	e := startEnv(t)
	type res struct {
		out string
		err error
	}
	got := make(chan res, 1)
	go func() {
		out, err := e.run("ask", "--from", "worker", "--timeout", "10s", "reviewer", "ok to merge?")
		got <- res{out, err}
	}()
	var mid string
	deadline := time.Now().Add(5 * time.Second)
	for mid == "" && time.Now().Before(deadline) {
		var rr struct{ Messages []struct{ MID, Kind string } }
		json.Unmarshal([]byte(e.must("read", "-w", "reviewer", "--peek", "--json")), &rr)
		if len(rr.Messages) == 1 && rr.Messages[0].Kind == "ask" {
			mid = rr.Messages[0].MID
		}
		time.Sleep(20 * time.Millisecond)
	}
	if mid == "" {
		t.Fatal("ask never landed")
	}
	e.must("reply", "--from", "reviewer", mid, "yes, merge it")
	select {
	case r := <-got:
		if r.err != nil || r.out != "yes, merge it\n" {
			t.Fatalf("%q %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask did not return")
	}
	out, err := e.run("ask", "--from", "worker", "--timeout", "200ms", "nobody", "anyone?")
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 || !strings.Contains(out, "timeout") {
		t.Fatalf("%v %q", err, out)
	}
	out, err = e.run("ask", "--from", "worker", "--timeout", "200ms", "nobody", "x", "--json")
	if err == nil || !strings.Contains(out, `"error":"timeout"`) {
		t.Fatalf("%v %q", err, out)
	}
}

func TestCLI_NoDaemonIsAClearError(t *testing.T) {
	e := &env{t: t, home: t.TempDir(), sock: filepath.Join(os.TempDir(), "mailai-none.sock")}
	out, err := e.run("ping")
	if err == nil || !strings.Contains(out, "no daemon at") {
		t.Fatalf("%v %q", err, out)
	}
}

func TestParse_FlagsAnywhere(t *testing.T) {
	fs := newFlags("x")
	sub := fs.String("subject", "", "")
	pos := parse(fs, []string{"worker", "--subject", "s", "some", "text", "--json"})
	if *sub != "s" || !jsonOut || strings.Join(pos, " ") != "worker some text" {
		t.Fatalf("%q %q %v", *sub, pos, jsonOut)
	}
	jsonOut = false
	fs = newFlags("y")
	pos = parse(fs, []string{"to", "--", "--not-a-flag", "x"})
	if strings.Join(pos, " ") != "to --not-a-flag x" {
		t.Fatalf("%q", pos)
	}
}

func TestCLI_AttachAndStash(t *testing.T) {
	e := startEnv(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "plan.md")
	os.WriteFile(src, []byte("plan"), 0o644)
	stored := strings.TrimSpace(e.must("stash", "put", src))
	if !strings.HasSuffix(stored, "-plan.md") {
		t.Fatal(stored)
	}
	e.must("send", "worker", "--attach", stored, "--attach", src, "here you go")
	os.Remove(src)
	out := e.must("read", "-w", "worker")
	if !strings.Contains(out, "attachments:\n  "+stored+" (4 B)\n  "+src+" (missing)\n") {
		t.Fatalf("%q", out)
	}
	if out := e.must("stash", "list"); !strings.HasPrefix(out, "plan.md\t4\t") {
		t.Fatal(out)
	}
	dest := filepath.Join(dir, "back.md")
	e.must("stash", "get", stored, dest)
	if b, _ := os.ReadFile(dest); string(b) != "plan" {
		t.Fatal(string(b))
	}
}

// SPEC §9: a token makes a process the agent it was issued to; the person,
// outside every pane, is verified.
func TestCLI_TokensPlaceAgents(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "helper", "--label", "test"), "\n", 2)[0])
	if !strings.HasPrefix(tok, "mt_") {
		t.Fatal(tok)
	}
	agent := e.cmd("whoami")
	agent.Env = append(agent.Env, "MAILAI_TOKEN="+tok)
	out, err := agent.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "helper (token)" {
		t.Fatalf("%q %v", out, err)
	}
	send := e.cmd("send", "human", "need a decision", "--json")
	send.Env = append(send.Env, "MAILAI_TOKEN="+tok)
	out, _ = send.CombinedOutput()
	if !strings.Contains(string(out), `"from": "helper"`) || !strings.Contains(string(out), `"sender_proof": "token"`) {
		t.Fatalf("%s", out)
	}
	// The agent cannot speak as the person, nor mint tokens.
	bad := e.cmd("send", "--from", "human", "helper", "approved")
	bad.Env = append(bad.Env, "MAILAI_TOKEN="+tok)
	if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "forbidden") {
		t.Fatalf("%q %v", out, err)
	}
	mint := e.cmd("token", "new", "evil")
	mint.Env = append(mint.Env, "MAILAI_TOKEN="+tok)
	if out, err := mint.CombinedOutput(); err == nil || !strings.Contains(string(out), "not_human") {
		t.Fatalf("%q %v", out, err)
	}
	// The person's reply shows as verified in the fence the agent reads.
	e.must("send", "helper", "go ahead")
	read := e.cmd("read")
	read.Env = append(read.Env, "MAILAI_TOKEN="+tok)
	out, _ = read.CombinedOutput()
	if !strings.Contains(string(out), "from human (verified human)") {
		t.Fatalf("%s", out)
	}
	if out := e.must("token", "ls"); !strings.HasPrefix(out, "helper\t") || !strings.HasSuffix(strings.TrimSpace(out), "test") {
		t.Fatal(out)
	}
	e.must("token", "revoke", "helper")
	agent = e.cmd("whoami")
	agent.Env = append(agent.Env, "MAILAI_TOKEN="+tok)
	out, _ = agent.CombinedOutput()
	if strings.TrimSpace(string(out)) != "human (outside_pane)" {
		t.Fatalf("revoked token: %q", out)
	}
}

// D13: the Inbox lists what waits on the person; ask-human blocks the agent's
// process and prints the chosen answer alone.
func TestCLI_InboxAskHumanAnswerDismiss(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "worker"), "\n", 2)[0])
	agent := func(args ...string) *exec.Cmd {
		c := e.cmd(args...)
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		return c
	}
	if out := e.must("inbox"); !strings.HasPrefix(out, "inbox empty") {
		t.Fatal(out)
	}
	// Plain mail to the person is not an Inbox item.
	if out, err := agent("send", "human", "fyi, tests pass").CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	if out := e.must("inbox"); !strings.HasPrefix(out, "inbox empty") {
		t.Fatal(out)
	}
	type res struct {
		out string
		err error
	}
	got := make(chan res, 1)
	go func() {
		out, err := agent("ask-human", "Deploy to staging?", "-o", "yes", "-o", "no", "--timeout", "10s").CombinedOutput()
		got <- res{string(out), err}
	}()
	var out string
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out, "Deploy to staging?") && time.Now().Before(deadline) {
		out = e.must("inbox")
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out, "#1  ask_human worker") || !strings.Contains(out, "[yes|no]") {
		t.Fatalf("%q", out)
	}
	if o, err := e.run("answer", "1", "maybe"); err == nil || !strings.Contains(o, "must be one of") {
		t.Fatalf("%q %v", o, err)
	}
	// An agent may look at the Inbox but not act on it.
	if o, err := agent("dismiss", "1").CombinedOutput(); err == nil || !strings.Contains(string(o), "not_human") {
		t.Fatalf("%s %v", o, err)
	}
	e.must("answer", "#1", "yes")
	select {
	case r := <-got:
		if r.err != nil || r.out != "yes\n" {
			t.Fatalf("%q %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask-human did not return")
	}
	if out := e.must("inbox"); !strings.HasPrefix(out, "inbox empty") {
		t.Fatal(out)
	}
	if out := e.must("inbox", "--all"); !strings.Contains(out, "(answered)") {
		t.Fatal(out)
	}
	// --no-wait, then dismiss: the agent coming back is told, exit 1.
	o, _ := agent("ask-human", "Proceed?", "--no-wait").CombinedOutput()
	if !strings.Contains(string(o), "asked as item #2") {
		t.Fatalf("%s", o)
	}
	e.must("dismiss", "2")
	o, err := agent("ask-human", "--wait-for", "2").CombinedOutput()
	if err == nil || !strings.Contains(string(o), "without an answer") {
		t.Fatalf("%s %v", o, err)
	}
}

// SPEC §8: the hook reports state, prints the notice once in the form the
// harness takes, and never fails the agent.
func TestCLI_HookReportsStateAndNoticeOnce(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "worker"), "\n", 2)[0])
	hook := func(event, payload string) string {
		c := e.cmd("hook", "claude-code", event)
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		c.Stdin = strings.NewReader(payload)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("hook %s: %v %s", event, err, out)
		}
		return string(out)
	}
	if out := hook("SessionStart", `{"hook_event_name":"SessionStart"}`); out != "" {
		t.Fatalf("no mail, no output: %q", out)
	}
	if out := e.must("agents"); !strings.Contains(out, "worker") || !strings.Contains(out, "idle") || !strings.Contains(out, "claude-code") {
		t.Fatal(out)
	}
	e.must("send", "worker", "--subject", "secret subject", "secret body")
	// UserPromptSubmit: plain stdout is context.
	out := hook("UserPromptSubmit", `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`)
	if !strings.HasPrefix(out, "You have 1 unread mailai message from human. Read it with: mailai read --unread") || strings.Contains(out, "secret") {
		t.Fatalf("%q", out)
	}
	// Once per message: the next hook adds nothing.
	if out := hook("PostToolUse", `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`); out != "" {
		t.Fatalf("announced twice: %q", out)
	}
	// New mail on PostToolUse: the JSON form.
	e.must("send", "worker", "more")
	out = hook("PostToolUse", `{"hook_event_name":"PostToolUse"}`)
	if !strings.Contains(out, `"hookEventName":"PostToolUse"`) || !strings.Contains(out, `"additionalContext":"You have 2 unread`) {
		t.Fatalf("%q", out)
	}
	// Stop reports done with the first line of the last message as label.
	hook("Stop", `{"hook_event_name":"Stop","last_assistant_message":"All green.\nDetails..."}`)
	out = e.must("agents", "--json")
	if !strings.Contains(out, `"state": "done"`) || !strings.Contains(out, `"label": "All green."`) {
		t.Fatal(out)
	}
	// Unknown event, unknown harness, no daemon: quiet, exit 0 (never fail the agent).
	if out := hook("SomethingNew", `{}`); out != "" {
		t.Fatal(out)
	}
	c := e.cmd("hook", "claude-code", "Stop")
	c.Env = append(c.Env, "MAILAI_SOCKET=/nonexistent/sock")
	if out, err := c.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("%v %q", err, out)
	}
}

func TestCLI_HookHoldsApprovalUntilAnswered(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "worker"), "\n", 2)[0])
	type res struct {
		out string
		err error
	}
	got := make(chan res, 1)
	go func() {
		c := e.cmd("hook", "claude-code", "PermissionRequest")
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		c.Stdin = strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"rm -rf node_modules","description":"x"}}`)
		out, err := c.CombinedOutput()
		got <- res{string(out), err}
	}()
	var out string
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out, "approval") && time.Now().Before(deadline) {
		out = e.must("inbox")
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out, "#1  approval  worker") || !strings.Contains(out, "rm -rf node_modules") {
		t.Fatalf("%q", out)
	}
	e.must("answer", "1", "allow")
	select {
	case r := <-got:
		if r.err != nil || !strings.Contains(r.out, `"behavior":"allow"`) || !strings.Contains(r.out, `"hookEventName":"PermissionRequest"`) {
			t.Fatalf("%q %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook did not return")
	}
}

func TestCLI_IntegrationInstallStatusUninstall(t *testing.T) {
	e := startEnv(t)
	home := t.TempDir()
	run := func(args ...string) string {
		c := e.cmd(args...)
		c.Env = append(c.Env, "MAILAI_TEST_HOME="+home)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return string(out)
	}
	if out := run("integration", "status", "claude-code"); !strings.Contains(out, "not installed") {
		t.Fatal(out)
	}
	if out := run("integration", "install", "claude-code", "--command", "/opt/mailai"); !strings.Contains(out, "hooks written") {
		t.Fatal(out)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if !strings.Contains(string(b), "/opt/mailai hook claude-code PermissionRequest") {
		t.Fatalf("%s", b)
	}
	if out := run("integration", "status", "claude-code", "--command", "/opt/mailai"); !strings.Contains(out, "installed, current") {
		t.Fatal(out)
	}
	// Codex is manual: install explains on stderr and does not fail.
	if out := run("integration", "install", "codex"); !strings.Contains(out, "mailai hook codex Stop") {
		t.Fatal(out)
	}
	if out := run("integration", "uninstall", "claude-code"); !strings.Contains(out, "hooks removed") {
		t.Fatal(out)
	}
	if out := run("--skill"); !strings.Contains(out, "mailai read --unread") {
		t.Fatal(out)
	}
}

// SPEC §13a: the daemon keeps a machine key in its data dir, and the person
// lists and forgets machines.
func TestCLI_MachineKeyAndMachineVerbs(t *testing.T) {
	e := startEnv(t)
	fi, err := os.Stat(filepath.Join(e.home, "data", "machine.key"))
	if err != nil || fi.Size() != 32 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if out := e.must("machine", "ls"); strings.TrimSpace(out) != "no machines have linked here" {
		t.Fatalf("%q", out)
	}
	if out, err := e.run("machine", "forget", "s9"); err == nil || !strings.Contains(out, "not_found") {
		t.Fatalf("%q %v", out, err)
	}
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "worker"), "\n", 2)[0])
	agent := e.cmd("machine", "ls")
	agent.Env = append(agent.Env, "MAILAI_TOKEN="+tok)
	if out, err := agent.CombinedOutput(); err == nil || !strings.Contains(string(out), "not_human") {
		t.Fatalf("%q %v", out, err)
	}
}
