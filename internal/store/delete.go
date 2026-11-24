package store

import "time"

// Expire deletes every message read more than keep ago (SPEC §6 Keeping).
// Unread mail is never deleted by age; keep <= 0 keeps everything. It
// returns how many went.
func (s *Store) Expire(keep time.Duration) (int, error) {
	n, _, err := s.ExpireThreads(keep)
	return n, err
}

// ExpireThreads is Expire, also saying which threads lost mail (in the
// order first met), for the daemon's log.
func (s *Store) ExpireThreads(keep time.Duration) (int, []string, error) {
	if keep <= 0 {
		return 0, nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-keep).UnixNano()
	var threads []string
	seen := map[string]bool{}
	n, err := s.drop(func(m *Message) bool {
		if m.ReadAt == 0 || m.ReadAt > cutoff {
			return false
		}
		if !seen[m.ThreadID] {
			seen[m.ThreadID] = true
			threads = append(threads, m.ThreadID)
		}
		return true
	})
	if err != nil {
		return n, threads, err
	}
	// What expiry removed takes its hidden marks with it.
	return n, threads, s.forgetHidden()
}

// DeleteThread deletes every message in a thread, whatever mailbox holds
// it, read or not (the person's delete, SPEC §6).
func (s *Store) DeleteThread(thread string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drop(func(m *Message) bool { return m.ThreadID == thread })
}

// Delete deletes the messages with these mids; a mid not held is skipped.
func (s *Store) Delete(mids ...string) (int, error) {
	want := make(map[string]bool, len(mids))
	for _, mid := range mids {
		want[mid] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drop(func(m *Message) bool { return want[m.MID] })
}

// drop removes every message gone says so. Unlike eviction it leaves the
// evicted count alone. Called with s.mu held.
func (s *Store) drop(gone func(*Message) bool) (int, error) {
	n := 0
	for box, mb := range s.boxes {
		kept := mb.msgs[:0]
		var err error
		for i, m := range mb.msgs {
			if err != nil || !gone(m) {
				kept = append(kept, m)
				continue
			}
			if s.p != nil {
				if err = s.p.Evict(box, m.MID, mb.evicted); err != nil {
					kept = append(kept, mb.msgs[i])
					continue
				}
			}
			mb.bytes -= m.size()
			delete(s.byMID, m.MID)
			n++
		}
		for i := len(kept); i < len(mb.msgs); i++ {
			mb.msgs[i] = nil
		}
		mb.msgs = kept
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
