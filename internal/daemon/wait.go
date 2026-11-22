package daemon

import (
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// WaitArgs: wait for mail in one's own mailbox.
type WaitArgs struct {
	// Mailbox to wait on, under read's rules; the caller's own by default.
	Mailbox store.Address `json:"mailbox,omitempty"`
	// TimeoutMS gives up after this long with the timeout code; forever
	// when zero.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// waitMail blocks until the caller's own mailbox holds unread mail, then
// returns it read, as read --unread does. It is how an agent waits for mail
// that is not the answer to an ask, instead of polling: the process sleeps
// on its connection and costs nothing (SPEC §7 point 5).
func (d *Daemon) waitMail(c *conn, a WaitArgs) (any, error) {
	box, err := d.box(c, a.Mailbox)
	if err != nil {
		return nil, err
	}
	ch := make(chan struct{}, 1)
	d.mu.Lock()
	d.mailWaits[box] = append(d.mailWaits[box], ch)
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		ws := d.mailWaits[box]
		for i, w := range ws {
			if w == ch {
				d.mailWaits[box] = append(ws[:i:i], ws[i+1:]...)
				break
			}
		}
		if len(d.mailWaits[box]) == 0 {
			delete(d.mailWaits, box)
		}
		d.mu.Unlock()
	}()
	var timer <-chan time.Time
	if a.TimeoutMS > 0 {
		t := time.NewTimer(time.Duration(a.TimeoutMS) * time.Millisecond)
		defer t.Stop()
		timer = t.C
	}
	for {
		// Registered first, so mail landing between this check and the
		// select still wakes it.
		if n, _ := d.st.Unread(box); n > 0 {
			return d.read(c, ReadArgs{Mailbox: box, Unread: true})
		}
		select {
		case <-ch:
		case <-timer:
			return nil, proto.Errorf(proto.CodeTimeout, "no mail yet")
		case <-c.gone:
			return nil, errClientGone
		case <-d.quitCh():
			return nil, proto.Errorf(proto.CodeOffline, "the daemon is stopping")
		}
	}
}
