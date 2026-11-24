package store

import "strings"

// Hidden for the person (SPEC §6, decided 2026-11-24): the person's delete
// hides a thread, or one message, from the person's own views. The
// messages stay where they are, for the agents, until expiry removes them.
// New mail in a hidden thread stays hidden unless it is to or from the
// person, which brings the thread back.

// HiddenPersister keeps hidden marks across restarts; a Persister may
// implement it.
type HiddenPersister interface {
	LoadHidden() (threads, mids map[string]int64, err error)
	PutHidden(kind, id string, at int64) error
	DeleteHidden(kind, id string) error
}

// The kinds of hidden mark.
const (
	hideThread  = "thread"
	hideMessage = "message"
)

func (s *Store) hiddenLocked(m *Message) bool {
	return s.hiddenThreads[m.ThreadID] != 0 || s.hiddenMsgs[m.MID] != 0
}

// Hidden says the person hid this message, or its thread.
func (s *Store) Hidden(m Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hiddenLocked(&m)
}

func (s *Store) putHidden(kind, id string, at int64) error {
	if hp, ok := s.p.(HiddenPersister); ok {
		return hp.PutHidden(kind, id, at)
	}
	return nil
}

func (s *Store) deleteHidden(kind, id string) error {
	if hp, ok := s.p.(HiddenPersister); ok {
		return hp.DeleteHidden(kind, id)
	}
	return nil
}

// HideThread hides a thread from the person; n is how many held messages
// that hid (0: none held, or all already hidden).
func (s *Store) HideThread(thread string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.byMID {
		if m.ThreadID == thread && !s.hiddenLocked(m) {
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	at := s.now().UnixNano()
	if err := s.putHidden(hideThread, thread, at); err != nil {
		return 0, err
	}
	s.hiddenThreads[thread] = at
	return n, nil
}

// HideMessages hides these messages from the person; a mid not held, or
// already hidden, is skipped.
func (s *Store) HideMessages(mids ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	at := s.now().UnixNano()
	for _, mid := range mids {
		m, ok := s.byMID[mid]
		if !ok || s.hiddenLocked(m) {
			continue
		}
		if err := s.putHidden(hideMessage, mid, at); err != nil {
			return n, err
		}
		s.hiddenMsgs[mid] = at
		n++
	}
	return n, nil
}

// isPersonAddr says an address is the person's, on any machine.
func isPersonAddr(a Address) bool {
	return a == Human || strings.HasPrefix(string(a), string(Human)+"@")
}

// unhideFor brings a hidden thread back when new mail in it is to or from
// the person. Called with s.mu held.
func (s *Store) unhideFor(m *Message) error {
	if s.hiddenThreads[m.ThreadID] == 0 || !(isPersonAddr(m.From) || isPersonAddr(m.To)) {
		return nil
	}
	if err := s.deleteHidden(hideThread, m.ThreadID); err != nil {
		return err
	}
	delete(s.hiddenThreads, m.ThreadID)
	return nil
}

// forgetHidden drops the marks of messages and threads no longer held.
// Called with s.mu held.
func (s *Store) forgetHidden() error {
	held := map[string]bool{}
	for _, m := range s.byMID {
		held[m.ThreadID] = true
	}
	for mid := range s.hiddenMsgs {
		if _, ok := s.byMID[mid]; !ok {
			if err := s.deleteHidden(hideMessage, mid); err != nil {
				return err
			}
			delete(s.hiddenMsgs, mid)
		}
	}
	for th := range s.hiddenThreads {
		if !held[th] {
			if err := s.deleteHidden(hideThread, th); err != nil {
				return err
			}
			delete(s.hiddenThreads, th)
		}
	}
	return nil
}
