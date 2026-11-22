// Package notify pops a desktop notification for the person (SPEC §12b).
//
// Every backend runs a program with a fixed argv: text is only ever a whole
// argument, never part of a shell command line or of script source. Every
// run is asynchronous and killed after a timeout, so a slow or broken
// backend never holds up the daemon.
package notify

import (
	"context"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Notifier shows one notification. It returns at once.
type Notifier interface {
	Notify(title, body string)
}

// Nop notifies nobody.
type Nop struct{}

func (Nop) Notify(string, string) {}

// Runner runs a program with its arguments until it exits or ctx ends.
type Runner func(ctx context.Context, name string, args ...string) error

// ExecRun runs the program directly, never through a shell, with no stdin,
// stdout or stderr.
func ExecRun(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// MaxBody bounds a notification body in bytes.
const MaxBody = 120

// DefaultTimeout bounds one notification run.
const DefaultTimeout = 5 * time.Second

// Config chooses a backend.
type Config struct {
	// Enabled is [notify] enabled; false is Nop.
	Enabled bool
	// Command is [notify] command, an argv. Title and body replace the
	// placeholders {title} and {body} wherever they appear; with neither
	// placeholder, title and body are appended as the last two arguments.
	Command []string
	// GOOS and LookPath default to runtime.GOOS and exec.LookPath.
	GOOS     string
	LookPath func(string) (string, error)
	// Run defaults to ExecRun; Timeout to DefaultTimeout.
	Run     Runner
	Timeout time.Duration
	Log     *log.Logger
}

// Choose picks the backend: the configured command, else osascript on
// macOS or notify-send on Linux when installed, else Nop. The string names
// the choice for the daemon log.
func Choose(c Config) (Notifier, string) {
	if !c.Enabled {
		return Nop{}, "off"
	}
	if c.GOOS == "" {
		c.GOOS = runtime.GOOS
	}
	if c.LookPath == nil {
		c.LookPath = exec.LookPath
	}
	if c.Run == nil {
		c.Run = ExecRun
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	mk := func(argv func(title, body string) []string) *execNotifier {
		return &execNotifier{argv: argv, run: c.Run, timeout: c.Timeout, log: c.Log}
	}
	if len(c.Command) > 0 && c.Command[0] != "" {
		cmd := append([]string(nil), c.Command...)
		if _, err := c.LookPath(cmd[0]); err != nil {
			return Nop{}, "none (command " + cmd[0] + " not found)"
		}
		return mk(func(t, b string) []string { return CommandArgv(cmd, t, b) }), "command " + cmd[0]
	}
	switch c.GOOS {
	case "darwin":
		if _, err := c.LookPath("osascript"); err == nil {
			return mk(OSAScriptArgv), "osascript"
		}
	case "linux":
		if _, err := c.LookPath("notify-send"); err == nil {
			return mk(NotifySendArgv), "notify-send"
		}
	}
	return Nop{}, "none"
}

// OSAScriptArgv shows a macOS notification. The AppleScript is fixed and
// reads title and body from its argv; "--" keeps text that starts with a
// dash from being read as an osascript option.
func OSAScriptArgv(title, body string) []string {
	return []string{"osascript",
		"-e", "on run argv",
		"-e", "display notification (item 1 of argv) with title (item 2 of argv)",
		"-e", "end run",
		"--", body, title}
}

// NotifySendArgv shows a Linux desktop notification through libnotify.
func NotifySendArgv(title, body string) []string {
	return []string{"notify-send", "--app-name=mailai", "--", title, body}
}

// CommandArgv is the configured command with title and body: substituted
// for {title} and {body} when either placeholder appears, else appended.
// Substitution is one pass, so text containing a placeholder stays as is.
func CommandArgv(cmd []string, title, body string) []string {
	placeholders := false
	for _, a := range cmd {
		if strings.Contains(a, "{title}") || strings.Contains(a, "{body}") {
			placeholders = true
		}
	}
	out := make([]string, 0, len(cmd)+2)
	if !placeholders {
		out = append(out, cmd...)
		return append(out, title, body)
	}
	r := strings.NewReplacer("{title}", title, "{body}", body)
	for _, a := range cmd {
		out = append(out, r.Replace(a))
	}
	return out
}

// execNotifier runs one argv per notification, each on its own goroutine.
type execNotifier struct {
	argv    func(title, body string) []string
	run     Runner
	timeout time.Duration
	log     *log.Logger
	wg      sync.WaitGroup
}

func (n *execNotifier) Notify(title, body string) {
	argv := n.argv(Clean(title, MaxBody), Clean(body, MaxBody))
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				n.logf("notify %s: panic: %v", argv[0], r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
		defer cancel()
		if err := n.run(ctx, argv[0], argv[1:]...); err != nil {
			n.logf("notify %s: %v", argv[0], err)
		}
	}()
}

func (n *execNotifier) logf(format string, a ...any) {
	if n.log != nil {
		n.log.Printf(format, a...)
	}
}

// Clean makes s one line of at most max bytes: control and bidi-override
// characters become spaces, runs of spaces collapse, invalid UTF-8 is
// dropped, and a cut ends in "…". Clean is idempotent.
func Clean(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidi(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	const ell = "…"
	cut := max - len(ell)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " ") + ell
}

// isBidi is a Unicode directional formatting character, which can make a
// line read differently from what it says.
func isBidi(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0x200E || r == 0x200F || r == 0x061C
}
