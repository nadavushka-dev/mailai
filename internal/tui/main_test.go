package tui

import (
	"os"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Tests run every command by hand (drain, or cmd() directly), never in a
// tea.Program, so a timer's only effect is the wait: the same messages
// arrive in the same order when it fires at once (the input cursor's blink
// too). This keeps the package
// fast without changing what any test asserts (todo #12: a start() drained
// ten real one-second ticks).
func TestMain(m *testing.M) {
	after = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(time.Now()) }
	}
	blinkSpeed = time.Nanosecond
	os.Exit(m.Run())
}
