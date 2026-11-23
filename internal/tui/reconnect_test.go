package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/daemon"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// netFake is the fake over a connection that can drop: while down every
// call says ErrNotConnected, and Reconnect brings it back unless dialErr.
type netFake struct {
	*fake
	down    bool
	dialErr error
	// events is handed out by the next Events call, then forgotten.
	events chan proto.Frame

	whoamis, inboxes, threadLoads, subs, redials int
}

func (f *netFake) Whoami() (daemon.WhoamiResult, error) {
	f.whoamis++
	if f.down {
		return daemon.WhoamiResult{}, ErrNotConnected
	}
	return f.fake.Whoami()
}

func (f *netFake) Inbox(all bool) ([]store.Item, error) {
	f.inboxes++
	if f.down {
		return nil, ErrNotConnected
	}
	return f.fake.Inbox(all)
}

func (f *netFake) Thread(id string) ([]store.Message, error) {
	f.threadLoads++
	if f.down {
		return nil, ErrNotConnected
	}
	return f.fake.Thread(id)
}

func (f *netFake) Events() <-chan proto.Frame {
	f.subs++
	ch := f.events
	f.events = nil
	if ch == nil {
		return nil
	}
	return ch
}

func (f *netFake) Reconnect() error {
	f.redials++
	if f.dialErr != nil {
		return f.dialErr
	}
	f.down = false
	return nil
}

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(Model), cmd
}

// Found in real use: restarting the daemon (an upgrade) killed the TUI and
// the person had to start it again. It keeps what it showed, says it is
// reconnecting, refuses to act meanwhile, and on reconnecting asks who it
// is again, subscribes again and fetches everything again.
func TestTUIReconnectsWhenTheDaemonComesBack(t *testing.T) {
	f := &netFake{fake: person()}
	m := start(f)
	m = press(m, "enter") // the thread of item 1
	if m.openID != "local:1" {
		t.Fatalf("thread not open: %q", m.openID)
	}

	// The daemon goes away: the event stream ends.
	ended := make(chan proto.Frame)
	close(ended)
	f.events, f.down = ended, true
	m, cmd := update(m, m.listen()())
	if !m.offline || cmd == nil {
		t.Fatalf("offline=%v cmd=%v: lost connection not noticed", m.offline, cmd != nil)
	}
	v := m.View()
	if !strings.Contains(v, "reconnecting to the daemon") || !strings.Contains(v, "really") {
		t.Fatalf("want old thread and a reconnecting line:\n%s", v)
	}

	// Acting while away is refused, plainly.
	m = press(m, "r")
	if m.mode != modeNone || !strings.Contains(m.View(), "not connected to the daemon") {
		t.Fatalf("mode=%v\n%s", m.mode, m.View())
	}
	m = press(m, "1", "x", "d")
	if len(f.answered)+len(f.dismiss)+len(f.replied) != 0 {
		t.Fatalf("acted while disconnected: %v %v %v", f.answered, f.dismiss, f.replied)
	}
	// The refusal fades back to the reconnecting line, not to nothing.
	later := m.now().Add(5 * time.Second)
	m.now = func() time.Time { return later }
	m, _ = update(m, tickMsg(later))
	if v := m.View(); !strings.Contains(v, "reconnecting to the daemon") || strings.Contains(v, "not connected") {
		t.Fatalf("%s", v)
	}

	// The first try comes after a short wait, and fails: still away.
	f.dialErr = errors.New("no daemon at /x/s")
	msg := cmd()
	m, cmd = update(m, msg)
	m, cmd = update(m, cmd())
	if f.redials != 1 || !m.offline || cmd == nil || !strings.Contains(m.View(), "reconnecting to the daemon") {
		t.Fatalf("redials=%d offline=%v cmd=%v\n%s", f.redials, m.offline, cmd != nil, m.View())
	}

	// The daemon is back, with news and another view of who this is.
	f.dialErr = nil
	f.items = append(f.items, store.Item{ID: 3, Kind: store.ItemAskHuman, From: "builder", Question: "Ship it?"})
	f.who = daemon.WhoamiResult{Principal: "w2Y:pY", Proof: store.ProofPane}
	whoamis, inboxes, threadLoads, subs := f.whoamis, f.inboxes, f.threadLoads, f.subs
	m, cmd = update(m, msg) // the next try
	m = drain(m, cmd)
	if m.offline || f.redials != 2 {
		t.Fatalf("offline=%v redials=%d", m.offline, f.redials)
	}
	if f.whoamis != whoamis+1 || f.inboxes <= inboxes || f.threadLoads <= threadLoads || f.subs != subs+1 {
		t.Fatalf("whoami %d→%d inbox %d→%d thread %d→%d subscribe %d→%d", whoamis, f.whoamis, inboxes, f.inboxes, threadLoads, f.threadLoads, subs, f.subs)
	}
	if m.who.Principal != "w2Y:pY" || m.person || len(m.items) != 3 || m.openID != "local:1" {
		t.Fatalf("who=%+v person=%v items=%d open=%q", m.who, m.person, len(m.items), m.openID)
	}
	if v := m.View(); strings.Contains(v, "reconnecting") || !strings.Contains(v, "reconnected") {
		t.Fatalf("%s", v)
	}
}

// A connection lost twice over (the stream of an old connection ending
// after a new one is up) must not start reconnecting again.
func TestAnOldConnectionEndingIsNotALoss(t *testing.T) {
	f := &netFake{fake: person()}
	m := start(f)
	ended := make(chan proto.Frame)
	close(ended)
	f.events = ended
	lost := m.listen()()
	m.gen++ // a reconnect happened meanwhile
	m, cmd := update(m, lost)
	if m.offline || cmd != nil {
		t.Fatalf("offline=%v cmd=%v", m.offline, cmd != nil)
	}
}

func TestQuitWhileDisconnected(t *testing.T) {
	f := &netFake{fake: person()}
	m := start(f)
	m.offline = true
	for _, k := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("q")}, {Type: tea.KeyCtrlC}} {
		_, cmd := update(m, k)
		if cmd == nil {
			t.Fatalf("%v: no quit", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%v: no quit", k)
		}
	}
}

func TestRedialBackoff(t *testing.T) {
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second}
	for i, w := range want {
		if got := redialDelay(i); got != w {
			t.Fatalf("try %d: %v, want %v", i, got, w)
		}
	}
}

// shortSock is a socket path short enough for macOS (104 bytes).
func shortSock(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	rand.Read(b)
	dir := filepath.Join(os.TempDir(), "mailai-t-"+hex.EncodeToString(b))
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// serve runs a daemon over st on sock; the returned func stops it.
func serve(t *testing.T, sock string, st *store.Store) (stop func()) {
	t.Helper()
	d := daemon.New(daemon.Options{Store: st})
	if err := d.Listen(sock); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Serve(ctx); close(done) }()
	stopped := false
	stop = func() {
		if !stopped {
			stopped = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return stop
}

// The Live backend over a real daemon that stops and starts again.
func TestLiveReconnectsToARestartedDaemon(t *testing.T) {
	sock := shortSock(t)
	st := store.New(store.Options{})
	stop := serve(t, sock, st)
	dial := func() (*client.Client, error) { return client.Dial(sock, "") }
	c, err := dial()
	if err != nil {
		t.Fatal(err)
	}
	l := NewLive(c, dial)
	t.Cleanup(func() { l.Close() })
	ev := l.Events()
	if ev == nil {
		t.Fatal("not subscribed")
	}

	stop()
	deadline := time.After(3 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-ev:
		case <-deadline:
			t.Fatal("the event stream did not end with the daemon")
		}
	}
	if _, err := l.Inbox(false); !errors.Is(err, ErrNotConnected) || err.Error() != "not connected to the daemon" {
		t.Fatalf("got %v", err)
	}
	if err := l.Answer(1, "yes", ""); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("got %v", err)
	}
	if err := l.Reconnect(); err == nil {
		t.Fatal("reconnected with no daemon")
	}

	serve(t, sock, st)
	if err := l.Reconnect(); err != nil {
		t.Fatal(err)
	}
	if w, err := l.Whoami(); err != nil || w.Proof != store.ProofOutsidePane {
		t.Fatalf("%+v %v", w, err)
	}
	ev = l.Events()
	other, err := client.Dial(sock, "")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Call("send", daemon.SendArgs{From: "worker", To: "human", Text: "back"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case f, ok := <-ev:
		if !ok {
			t.Fatal("event stream closed after reconnecting")
		}
		_ = f
	case <-time.After(3 * time.Second):
		t.Fatal("no event after reconnecting")
	}
	if th, err := l.Threads(); err != nil || len(th) != 1 {
		t.Fatalf("%v %v", th, err)
	}
}
