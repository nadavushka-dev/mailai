package daemon

import (
	"strconv"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// messageRef turns what a caller names a message by into its mid (SPEC
// §12). A bare number, optionally #-prefixed, is the number in the fence
// header: the id of a message in the caller's own mailbox. Anything else
// is taken as a mid (or a thread id) and passed through.
func (d *Daemon) messageRef(c *conn, ref string) (string, error) {
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
	}
	return "", proto.Errorf(proto.CodeNotFound, "no message #%d in your mailbox; pass the message's mid (machine:id) instead", n)
}
