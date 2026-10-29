package store

import (
	"errors"
	"time"
)

// Rate limiting (SPEC §6): a token bucket per claimed sender, and one shared
// overflow bucket for remote senders once MaxBuckets names are tracked, so a
// peer cannot dodge the limit by inventing names.
const (
	RateBurst  = 10
	RatePerSec = 1.0
	MaxBuckets = 256
)

var ErrRateLimited = errors.New("rate_limited")

const overflowKey Address = "\x00overflow"

type bucket struct {
	tokens float64
	last   time.Time
}

// limiter is owned by the Store and called under its lock.
type limiter struct {
	buckets map[Address]*bucket
}

func newLimiter() *limiter { return &limiter{buckets: map[Address]*bucket{}} }

// take spends one token for a sender, or reports ErrRateLimited. A local
// sender always gets its own bucket; a remote one shares overflow past the cap.
func (l *limiter) take(from Address, remote bool, now time.Time) error {
	key := from
	b, ok := l.buckets[key]
	if !ok {
		if remote && len(l.buckets) >= MaxBuckets {
			key = overflowKey
			b = l.buckets[key]
		}
		if b == nil {
			b = &bucket{tokens: RateBurst, last: now}
			l.buckets[key] = b
		}
	}
	if d := now.Sub(b.last); d > 0 {
		b.tokens += d.Seconds() * RatePerSec
		if b.tokens > RateBurst {
			b.tokens = RateBurst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return ErrRateLimited
	}
	b.tokens--
	return nil
}
