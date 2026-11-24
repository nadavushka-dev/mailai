package daemon

import (
	"context"
	"time"

	"github.com/nadavushka-dev/mailai/internal/proto"
)

// Deleting mail (SPEC §6): the person deletes a thread or messages by hand;
// read mail goes by itself after KeepFor. Agents cannot delete.

// DeleteArgs names a whole thread, or messages by mid.
type DeleteArgs struct {
	Thread string   `json:"thread,omitempty"`
	MIDs   []string `json:"mids,omitempty"`
}

type DeleteResult struct {
	Deleted int `json:"deleted"`
}

// DefaultExpireEvery is how often read mail past KeepFor is swept.
const DefaultExpireEvery = time.Hour

func (d *Daemon) deleteMail(c *conn, a DeleteArgs) (any, error) {
	if !isPerson(c.pl) {
		return nil, proto.Errorf(proto.CodeNotHuman, "only the person deletes mail")
	}
	if a.Thread == "" && len(a.MIDs) == 0 {
		return nil, proto.Errorf(proto.CodeBadRequest, "name a thread or messages to delete")
	}
	n := 0
	if a.Thread != "" {
		k, err := d.st.DeleteThread(a.Thread)
		n += k
		if err != nil {
			return nil, err
		}
	}
	if len(a.MIDs) > 0 {
		k, err := d.st.Delete(a.MIDs...)
		n += k
		if err != nil {
			return nil, err
		}
	}
	if n == 0 {
		return nil, proto.Errorf(proto.CodeNotFound, "nothing held here to delete")
	}
	d.publishEvent("deleted", map[string]any{"thread": a.Thread, "count": n})
	return DeleteResult{Deleted: n}, nil
}

// expire deletes read mail past KeepFor; zero keeps it until evicted.
func (d *Daemon) expire() {
	if d.keepFor <= 0 {
		return
	}
	n, err := d.st.Expire(d.keepFor)
	if err != nil {
		d.log.Printf("expire: %v", err)
	}
	if n > 0 {
		d.log.Printf("expired %d read message(s) older than %s", n, d.keepFor)
		d.publishEvent("deleted", map[string]any{"count": n})
	}
}

func (d *Daemon) runExpire(ctx context.Context) {
	if d.keepFor <= 0 {
		return
	}
	t := time.NewTicker(d.expireEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.expire()
		}
	}
}
