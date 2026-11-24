package daemon

import (
	"strconv"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// What to do instead when a header number names no message the caller
// holds or sent: each names only a way that works.
const (
	RefHintRead  = "pass its mid (machine:id) instead"
	RefHintReply = "reply with its mid (machine:id) and --to <recipient>"
)

// messageRef turns what a caller names a message by into its mid (SPEC
// §12). A bare number, optionally #-prefixed, is the number in the fence
// header: the id of a message in the caller's own mailbox, or of one the
// caller sent. Anything else is taken as a mid (or a thread id) and passed
// through. reply says the caller is replying, for the hint: a reply by an
// unknown number works with the mid and --to (a mid not held roots a new
// thread at it), a read --thread with the mid.
func (d *Daemon) messageRef(c *conn, ref string, reply bool) (string, error) {
	digits := strings.TrimPrefix(ref, "#")
	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return ref, nil
	}
	box := d.own(c.pl)
	if box != "" {
		msgs, err := d.st.Read(store.ReadQuery{Mailbox: box, IDs: []uint64{n}})
		if err != nil {
			return "", err
		}
		if len(msgs) == 1 {
			return msgs[0].MID, nil
		}
		if m, ok := d.st.SentBy(box, n); ok {
			return m.MID, nil
		}
	}
	if reply {
		return "", proto.Errorf(proto.CodeNotFound, "no message #%d in your mailbox or sent by you; %s", n, RefHintReply)
	}
	return "", proto.Errorf(proto.CodeNotFound, "no message #%d in your mailbox or sent by you; %s", n, RefHintRead)
}
