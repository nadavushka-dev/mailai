package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeRun records every argv it is asked to run instead of running it.
type fakeRun struct {
	mu    sync.Mutex
	calls [][]string
	got   chan []string
	block bool
}

func newFakeRun() *fakeRun { return &fakeRun{got: make(chan []string, 16)} }

func (f *fakeRun) run(ctx context.Context, name string, args ...string) error {
	argv := append([]string{name}, args...)
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	f.mu.Unlock()
	f.got <- argv
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakeRun) wait(t *testing.T) []string {
	t.Helper()
	select {
	case a := <-f.got:
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("nothing ran")
		return nil
	}
}

func noPath(string) (string, error)  { return "", errors.New("not found") }
func hasPath(string) (string, error) { return "/usr/bin/x", nil }

// hostile is sender-chosen text that would do harm in a shell or in
// AppleScript source.
const hostile = `"; do shell script "rm -rf ~"; " $(touch /tmp/pwn) ` + "`id`" + ` -e x`

// noShell checks an argv runs no shell and carries text only as whole
// arguments, never spliced into script source.
func noShell(t *testing.T, argv []string, texts ...string) {
	t.Helper()
	for _, a := range argv {
		switch a {
		case "sh", "bash", "zsh", "/bin/sh", "/bin/bash", "-c":
			t.Fatalf("argv runs a shell: %q", argv)
		}
	}
	for _, text := range texts {
		whole := false
		for _, a := range argv {
			if a == text {
				whole = true
			} else if strings.Contains(a, text) {
				t.Fatalf("text spliced into an argument: %q", a)
			}
		}
		if !whole {
			t.Fatalf("%q not passed as its own argument in %q", text, argv)
		}
	}
}

func TestOSAScript_TextOnlyThroughArgv(t *testing.T) {
	argv := OSAScriptArgv("mailai", hostile)
	if argv[0] != "osascript" {
		t.Fatalf("%q", argv)
	}
	noShell(t, argv, "mailai", hostile)
	// The script is fixed: it reads argv, and nothing after "--" is an option.
	want := []string{"osascript", "-e", "on run argv", "-e", "display notification (item 1 of argv) with title (item 2 of argv)", "-e", "end run", "--", hostile, "mailai"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %q\nwant %q", argv, want)
	}
}

func TestNotifySend_TextOnlyThroughArgv(t *testing.T) {
	argv := NotifySendArgv("the title", hostile)
	want := []string{"notify-send", "--app-name=mailai", "--", "the title", hostile}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %q\nwant %q", argv, want)
	}
	noShell(t, argv, "the title", hostile)
}

func TestCommand_AppendsOrSubstitutes(t *testing.T) {
	argv := CommandArgv([]string{"ntfy", "publish", "mytopic"}, "mailai", hostile)
	want := []string{"ntfy", "publish", "mytopic", "mailai", hostile}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %q\nwant %q", argv, want)
	}
	noShell(t, argv, hostile)
	// Placeholders: substituted in place, once, nothing appended.
	argv = CommandArgv([]string{"ntfy", "publish", "--title={title}", "mytopic", "{body}"}, "mailai", "a {title} b")
	want = []string{"ntfy", "publish", "--title=mailai", "mytopic", "a {title} b"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got  %q\nwant %q", argv, want)
	}
	// The configured slice is never modified.
	cfg := []string{"x", "{body}"}
	CommandArgv(cfg, "t", "b")
	if cfg[1] != "{body}" {
		t.Fatal("config mutated")
	}
}

func TestChoose_ByPlatformConfigAndAvailability(t *testing.T) {
	cases := []struct {
		name    string
		c       Config
		backend string
		argv0   string
	}{
		{"off", Config{Enabled: false, GOOS: "darwin", LookPath: hasPath}, "off", ""},
		{"darwin", Config{Enabled: true, GOOS: "darwin", LookPath: hasPath}, "osascript", "osascript"},
		{"linux", Config{Enabled: true, GOOS: "linux", LookPath: hasPath}, "notify-send", "notify-send"},
		{"linux without notify-send", Config{Enabled: true, GOOS: "linux", LookPath: noPath}, "none", ""},
		{"other os", Config{Enabled: true, GOOS: "plan9", LookPath: hasPath}, "none", ""},
		{"command", Config{Enabled: true, GOOS: "darwin", LookPath: hasPath, Command: []string{"ntfy", "publish", "t"}}, "command ntfy", "ntfy"},
		{"command missing", Config{Enabled: true, GOOS: "darwin", LookPath: noPath, Command: []string{"ntfy"}}, "none (command ntfy not found)", ""},
		{"command off", Config{Enabled: false, GOOS: "darwin", LookPath: hasPath, Command: []string{"ntfy"}}, "off", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRun()
			tc.c.Run = f.run
			n, backend := Choose(tc.c)
			if backend != tc.backend {
				t.Fatalf("backend %q, want %q", backend, tc.backend)
			}
			n.Notify("mailai", "hello")
			if tc.argv0 == "" {
				if _, ok := n.(Nop); !ok {
					t.Fatalf("%T is not a no-op", n)
				}
				return
			}
			if argv := f.wait(t); argv[0] != tc.argv0 {
				t.Fatalf("%q", argv)
			} else {
				noShell(t, argv, "hello")
			}
		})
	}
}

// Notify never blocks the caller, and a stuck backend is killed at the
// timeout.
func TestNotify_AsyncWithTimeout(t *testing.T) {
	f := newFakeRun()
	f.block = true
	n, _ := Choose(Config{Enabled: true, GOOS: "linux", LookPath: hasPath, Run: f.run, Timeout: 50 * time.Millisecond})
	start := time.Now()
	n.Notify("mailai", "x")
	if time.Since(start) > 20*time.Millisecond {
		t.Fatal("Notify blocked")
	}
	f.wait(t)
	done := make(chan struct{})
	go func() { n.(*execNotifier).wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a stuck backend outlived its timeout")
	}
	// A panicking runner does not take the process down.
	p, _ := Choose(Config{Enabled: true, GOOS: "linux", LookPath: hasPath, Run: func(context.Context, string, ...string) error { panic("boom") }})
	p.Notify("mailai", "x")
	p.(*execNotifier).wg.Wait()
}

func TestNotify_DefaultTimeoutIsFiveSeconds(t *testing.T) {
	n, _ := Choose(Config{Enabled: true, GOOS: "linux", LookPath: hasPath, Run: newFakeRun().run})
	if n.(*execNotifier).timeout != 5*time.Second {
		t.Fatal(n.(*execNotifier).timeout)
	}
}

func TestClean_StripsControlsAndBoundsBytes(t *testing.T) {
	if got := Clean("a\x1b[31mred\x07\r\nnext\tline\u202eevil", 120); got != "a [31mred next line evil" {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("é", 200)
	got := Clean(long, 120)
	if len(got) > 120 || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("%d %q", len(got), got)
	}
	if Clean(got, 120) != got {
		t.Fatal("Clean is not idempotent")
	}
	if got := Clean("bad\xffutf8", 120); !utf8.ValidString(got) {
		t.Fatalf("%q", got)
	}
	if Clean("short", 120) != "short" {
		t.Fatal("short text changed")
	}
}
