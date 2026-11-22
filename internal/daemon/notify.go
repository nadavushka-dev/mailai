package daemon

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nadavushka-dev/mailai/internal/notify"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Notifications (SPEC §12b): when an Inbox item opens on this machine the
// person is told once, in one line. A burst is one notification at once and
// one summary when the window ends; items closed meanwhile are not counted.

const notifyTitle = "mailai"

// alerter coalesces item notifications to at most one per window.
type alerter struct {
	n     notify.Notifier
	every time.Duration
	open  func(id uint64) bool

	mu      sync.Mutex
	last    time.Time
	pending []alert
	timer   *time.Timer
	stopped bool
}

type alert struct {
	id   uint64
	body string
}

func newAlerter(n notify.Notifier, every time.Duration, open func(uint64) bool) *alerter {
	return &alerter{n: n, every: every, open: open}
}

// add notifies at once when the window is clear, else holds it for the
// summary at the end of the window.
func (a *alerter) add(id uint64, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return
	}
	now := time.Now()
	if a.timer == nil && (a.last.IsZero() || now.Sub(a.last) >= a.every) {
		a.last = now
		a.n.Notify(notifyTitle, body)
		return
	}
	a.pending = append(a.pending, alert{id, body})
	if a.timer == nil {
		a.timer = time.AfterFunc(a.last.Add(a.every).Sub(now), a.flush)
	}
}

// flush sends what is still open from the window: the first, and how many
// more wait.
func (a *alerter) flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.pending
	a.pending, a.timer = nil, nil
	if a.stopped {
		return
	}
	var live []alert
	for _, p := range pending {
		if a.open(p.id) {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		return
	}
	body := live[0].body
	if more := len(live) - 1; more > 0 {
		suffix := fmt.Sprintf(" (+%d more waiting)", more)
		body = notify.Clean(body, notify.MaxBody-len(suffix)) + suffix
	}
	a.last = time.Now()
	a.n.Notify(notifyTitle, body)
}

func (a *alerter) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	if a.timer != nil {
		a.timer.Stop()
	}
	a.pending, a.timer = nil, nil
}

// itemOpened tells the person about a new item on this machine.
func (d *Daemon) itemOpened(it store.Item) {
	if d.alerts != nil {
		d.alerts.add(it.ID, alertBody(it))
	}
}

func (d *Daemon) itemIsOpen(id uint64) bool {
	it, ok := d.st.Item(id)
	return ok && it.ClosedAt == 0
}

// alertBody is one line: kind, who waits, the question, and the options of
// an ask_human, at most notify.MaxBody bytes with no control characters.
// The question is cut first so the options stay visible.
func alertBody(it store.Item) string {
	kind := string(it.Kind)
	q := it.Question
	switch it.Kind {
	case store.ItemApproval:
		if it.Risky {
			kind = "risky approval"
		}
		if it.Subject != "" {
			q = it.Subject + ": " + q
		}
	case store.ItemAsk:
		if it.Subject != "" {
			q = it.Subject
		}
	}
	prefix := notify.Clean(kind+" from "+string(it.From)+":", notify.MaxBody/2) + " "
	suffix := ""
	if it.Kind == store.ItemAskHuman && len(it.Options) > 0 {
		suffix = " " + notify.Clean("["+strings.Join(it.Options, "|")+"]", notify.MaxBody/3)
	}
	q = notify.Clean(q, notify.MaxBody-len(prefix)-len(suffix))
	return notify.Clean(prefix+q+suffix, notify.MaxBody)
}
