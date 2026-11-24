package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nadavushka-dev/mailai/internal/harness"
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
	return startEnvWith(t, "")
}

// startEnvWith adds lines to the test daemon's config.toml.
func startEnvWith(t *testing.T, extra string) *env {
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
	os.WriteFile(filepath.Join(home, "config", "config.toml"), []byte("[identity]\nrequire_terminal = false\ndetect_agents = false\n[notify]\nenabled = false\n"+extra), 0o600)
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

// wait blocks until mail arrives and prints it as read --unread does; with
// nothing it exits 2 like ask, so an agent never has to poll unread.
func TestCLI_WaitPrintsMailAndTimesOutWithExit2(t *testing.T) {
	e := startEnv(t)
	got := make(chan string, 1)
	go func() {
		out, _ := e.run("wait", "-w", "worker", "--timeout", "10s")
		got <- out
	}()
	time.Sleep(200 * time.Millisecond)
	e.must("send", "--from", "reviewer", "worker", "the rate is 0.92")
	select {
	case out := <-got:
		if !strings.Contains(out, "the rate is 0.92") || !strings.Contains(out, "from reviewer") {
			t.Fatalf("%q", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
	}
	out, err := e.run("wait", "-w", "worker", "--timeout", "200ms")
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 || !strings.Contains(out, "timeout") {
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

// v0: with [web] port the page is up from the start; mailai web prints the
// login link at [web] url and the same link on this machine.
func TestCLI_WebServedFromTheStart(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	e := startEnvWith(t, fmt.Sprintf("[web]\nport = %d\nurl = \"https://box.tail0.ts.net\"\n", port))
	r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("page not served from the start: %v", err)
	}
	r.Body.Close()
	out := e.must("web")
	if !strings.HasPrefix(out, "https://box.tail0.ts.net/?t=") || !strings.Contains(out, fmt.Sprintf("http://127.0.0.1:%d/?t=", port)) {
		t.Fatalf("%q", out)
	}
}

// `mailai addon pi DIR` writes the Pi add-on (it runs mailai listen and
// hands each line to Pi) and the skill beside it, for `pi -e DIR`.
func TestCLI_AddonPiWritesTheAddonAndSkill(t *testing.T) {
	e := startEnvWith(t, "[addon]\nagents = [\"builder\"]\n")
	dir := filepath.Join(t.TempDir(), "pi")
	out := e.must("addon", "pi", dir)
	if !strings.Contains(out, "pi -e "+dir) {
		t.Fatalf("says how to load it: %q", out)
	}
	ts, err := os.ReadFile(filepath.Join(dir, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"listen"`, "sendMessage", `deliverAs: "followUp"`, "triggerTurn: true", "session_shutdown", "resources_discover"} {
		if !strings.Contains(string(ts), want) {
			t.Fatalf("add-on lacks %s", want)
		}
	}
	sk, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil || !strings.Contains(string(sk), "name: mailai") {
		t.Fatalf("skill: %v %q", err, sk)
	}
	e.must("addon", "pi", dir) // again: idempotent
	// The add-on asks `mailai listen --check` whether its agent is switched
	// on: exit 0 yes, exit 3 no (and it stays out of that session).
	for _, tc := range []struct {
		name string
		want int
	}{{"builder", 0}, {"brain", 3}} {
		tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", tc.name), "\n", 2)[0])
		c := e.cmd("listen", "--check")
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		c.Run()
		if got := c.ProcessState.ExitCode(); got != tc.want {
			t.Fatalf("listen --check as %s: exit %d, want %d", tc.name, got, tc.want)
		}
	}
	if _, err := e.run("addon", "nope", dir); err == nil {
		t.Fatal("an unknown harness is refused")
	}
}

// SPEC §7 (2026-11-23): `mailai listen` stays open and prints one wake line
// per wake, for a harness add-on to hand its agent; nothing is typed.
func TestCLI_ListenPrintsTheWakeLine(t *testing.T) {
	e := startEnvWith(t, "[addon]\nagents = [\"builder\"]\n")
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "builder"), "\n", 2)[0])
	l := e.cmd("listen")
	l.Env = append(l.Env, "MAILAI_TOKEN="+tok)
	out, err := l.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Process.Kill(); l.Wait() })
	lines := make(chan string, 4)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	time.Sleep(300 * time.Millisecond) // listening before the mail lands
	e.must("send", "--from", "reviewer", "builder", "the diff is ready")
	select {
	case line := <-lines:
		if !strings.Contains(line, "reviewer") || strings.Contains(line, "diff is ready") {
			t.Fatalf("wake line %q: names the sender, never the text", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no wake line")
	}
	// The line only announces: the mail is still unread.
	c := e.cmd("unread")
	c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
	if o, _ := c.CombinedOutput(); !strings.Contains(string(o), "1") {
		t.Fatalf("unread after the wake: %q", o)
	}
}

// An empty Inbox for a caller that is not the person says so, not "nothing
// waits on you" (found on UbCl, where a question was waiting).
func TestCLI_InboxSaysWhenYouAreNotThePerson(t *testing.T) {
	e := startEnv(t)
	e.must("ask-human", "--from", "worker", "--no-wait", "-o", "yes", "-o", "no", "ok?")
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "builder"), "\n", 2)[0])
	c := e.cmd("inbox")
	c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
	out, err := c.CombinedOutput()
	if err != nil || strings.Contains(string(out), "inbox empty") || !strings.Contains(string(out), "not the person") {
		t.Fatalf("%v %q", err, out)
	}
	c = e.cmd("whoami")
	c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
	if out, _ := c.CombinedOutput(); strings.TrimSpace(string(out)) != "builder (token)" {
		t.Fatalf("an agent's whoami stays one short line: %q", out)
	}
}

// An agent attaches files to a question for the person, by a path relative
// to where it runs; the item carries the absolute reference.
// SPEC §7a (2026-11-23): an agent takes back its own question with
// withdraw, or asks again with --replaces.
func TestCLI_WithdrawAndReplaces(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "builder"), "\n", 2)[0])
	agent := func(args ...string) (string, error) {
		c := e.cmd(args...)
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	for _, q := range []string{"Layout ok?", "Euro or dollar?"} {
		if out, err := agent("ask-human", "--no-wait", q); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	if out, err := agent("withdraw", "#2"); err != nil || !strings.Contains(out, "withdrew #2") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := agent("ask-human", "--no-wait", "--replaces", "1", "Layout ok? (file attached)"); err != nil || !strings.Contains(out, "replaces #1") {
		t.Fatalf("%v\n%s", err, out)
	}
	all := e.must("inbox", "--all")
	if strings.Count(all, "(withdrawn)") != 2 || !strings.Contains(all, "#3") {
		t.Fatalf("%s", all)
	}
	if out, err := agent("withdraw", "2"); err == nil {
		t.Fatalf("withdrew twice: %s", out)
	}
}

// SPEC §6: the person deletes a thread, or one message, from the CLI.
func TestCLI_Delete(t *testing.T) {
	e := startEnv(t)
	midOf := func(out string) string {
		// sent #1 (m:1) to worker
		i, j := strings.Index(out, "("), strings.Index(out, ")")
		if i < 0 || j < i {
			t.Fatalf("no mid in %q", out)
		}
		return out[i+1 : j]
	}
	e.must("send", "worker", "keep me")
	root := midOf(e.must("send", "worker", "delete this thread"))
	one := midOf(e.must("send", "worker", "delete this message"))
	if out := e.must("delete", root); !strings.Contains(out, "deleted 1 message") {
		t.Fatalf("%s", out)
	}
	if out := e.must("delete", "--message", one); !strings.Contains(out, "deleted 1 message") {
		t.Fatalf("%s", out)
	}
	left := e.must("read", "--peek", "-w", "worker")
	if !strings.Contains(left, "keep me") || strings.Contains(left, "delete this") {
		t.Fatalf("left:\n%s", left)
	}
	if out, err := e.cmd("delete", root).CombinedOutput(); err == nil {
		t.Fatalf("deleted twice: %s", out)
	}
	if out, err := e.cmd("delete").CombinedOutput(); err == nil || !strings.Contains(string(out), "usage: mailai delete") {
		t.Fatalf("%v %s", err, out)
	}
}

// SPEC §12: `mailai reply 54` (or '#54') answers the message whose fence
// header says #54, in its thread; agents never see mids.
func TestCLI_ReplyByHeaderNumber(t *testing.T) {
	e := startEnv(t)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "builder"), "\n", 2)[0])
	as := func(args ...string) (string, error) {
		c := e.cmd(args...)
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		out, err := c.CombinedOutput()
		return string(out), err
	}
	var sent struct {
		ID  uint64 `json:"id"`
		MID string `json:"mid"`
	}
	if err := json.Unmarshal([]byte(e.must("send", "--from", "reviewer", "builder", "please review", "--json")), &sent); err != nil || sent.ID == 0 {
		t.Fatal(err, sent)
	}
	n := fmt.Sprint(sent.ID)
	if out, _ := as("read"); !strings.Contains(out, "#"+n+" from reviewer") {
		t.Fatalf("header: %s", out)
	}
	for _, ref := range []string{n, "#" + n} {
		out, err := as("reply", ref, "looks good")
		if err != nil || !strings.Contains(out, "to reviewer in thread "+sent.MID) {
			t.Fatalf("reply %s: %v %s", ref, err, out)
		}
	}
	out, err := as("reply", "999", "x")
	if err == nil || !strings.Contains(out, "#999") || !strings.Contains(out, "mid") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestCLI_AskHumanAttachesRelativePaths(t *testing.T) {
	e := startEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "shot.png"), []byte("png"), 0o644)
	tok := strings.TrimSpace(strings.SplitN(e.must("token", "new", "builder"), "\n", 2)[0])
	agent := func(args ...string) *exec.Cmd {
		c := e.cmd(args...)
		c.Env = append(c.Env, "MAILAI_TOKEN="+tok)
		c.Dir = dir
		return c
	}
	c := agent("ask-human", "--no-wait", "--attach", "shot.png", "-o", "ship", "-o", "redo", "Ship this design?")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	c = agent("send", "human", "--attach", "shot.png", "also here")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var in struct {
		Items []struct {
			Attachments []struct{ Path string }
		}
	}
	out := e.must("inbox", "--json")
	json.Unmarshal([]byte(out), &in)
	if len(in.Items) != 1 || len(in.Items[0].Attachments) != 1 {
		t.Fatalf("inbox: %s", out)
	}
	got, _ := filepath.EvalSymlinks(in.Items[0].Attachments[0].Path)
	want, _ := filepath.EvalSymlinks(filepath.Join(dir, "shot.png"))
	if !filepath.IsAbs(in.Items[0].Attachments[0].Path) || got != want {
		t.Fatalf("path %q, want %q", in.Items[0].Attachments[0].Path, want)
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

// SPEC §8: install delivers the guide as an Agent Skill; --project puts it
// (and the Claude Code hooks) in one repo, --skill-only leaves hooks alone,
// and --skill still prints the bare guide. Needs no daemon.
func TestCLI_IntegrationSkill(t *testing.T) {
	e := &env{t: t, home: t.TempDir(), sock: filepath.Join(t.TempDir(), "none.sock")}
	home, proj := t.TempDir(), t.TempDir()
	run := func(args ...string) string {
		c := e.cmd(args...)
		c.Env = append(c.Env, "MAILAI_TEST_HOME="+home)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return string(out)
	}
	claude := filepath.Join(".claude", "skills", "mailai", "SKILL.md")
	agents := filepath.Join(".agents", "skills", "mailai", "SKILL.md")

	out := run("integration", "install", "--project", proj, "--command", "/opt/mailai")
	if !strings.Contains(out, "skill written to "+filepath.Join(proj, claude)) || !strings.Contains(out, "skill written to "+filepath.Join(proj, agents)) {
		t.Fatal(out)
	}
	for _, p := range []string{claude, agents} {
		b, err := os.ReadFile(filepath.Join(proj, p))
		if err != nil || !strings.HasPrefix(string(b), "---\nname: mailai\n") || !strings.Contains(string(b), "mailai read --unread") {
			t.Fatalf("%v %s", err, b)
		}
	}
	b, _ := os.ReadFile(filepath.Join(proj, ".claude", "settings.json"))
	if !strings.Contains(string(b), "/opt/mailai hook claude-code PermissionRequest") {
		t.Fatalf("project hooks: %s", b)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("--project wrote to the home: %v", ents)
	}
	if out := run("integration", "install", "--project", proj, "--command", "/opt/mailai"); strings.Count(out, "skill already current") != 2 {
		t.Fatal(out)
	}
	out = run("integration", "status", "claude-code", "--project", proj, "--command", "/opt/mailai")
	if !strings.Contains(out, filepath.Join(proj, claude)) || strings.Count(out, "installed, current") != 2 {
		t.Fatal(out)
	}

	// --skill-only: the skill goes, the hooks stay.
	if out := run("integration", "uninstall", "--skill-only", "--project", proj); strings.Count(out, "skill removed from") != 2 || strings.Contains(out, "hooks") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(proj, claude)); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(proj, ".claude", "settings.json")); !strings.Contains(string(b), "hook claude-code") {
		t.Fatalf("%s", b)
	}

	// User level, skill only: no settings.json, no manual codex wiring.
	out = run("integration", "install", "--skill-only")
	if strings.Contains(out, "mailai hook codex") || strings.Contains(out, "hooks") {
		t.Fatal(out)
	}
	for _, p := range []string{claude, agents} {
		if _, err := os.Stat(filepath.Join(home, p)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("--skill-only wrote hooks", err)
	}
	out = run("integration", "status", "--skill-only")
	if strings.Count(out, "installed, current") != 2 || strings.Contains(out, "settings.json") {
		t.Fatal(out)
	}
	// A user's own mailai skill is left alone.
	os.WriteFile(filepath.Join(home, agents), []byte("my notes\n"), 0o644)
	if out := run("integration", "uninstall", "--skill-only", "codex"); !strings.Contains(out, "not written by mailai") {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(filepath.Join(home, agents)); string(b) != "my notes\n" {
		t.Fatalf("%s", b)
	}

	if out := run("--skill"); out != harness.Skill {
		t.Fatalf("--skill must print the bare guide: %q", out)
	}
	// --project needs a directory that exists.
	c := e.cmd("integration", "install", "--project", filepath.Join(proj, "nope"))
	c.Env = append(c.Env, "MAILAI_TEST_HOME="+home)
	if out, err := c.CombinedOutput(); err == nil {
		t.Fatalf("accepted a missing project dir: %s", out)
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
