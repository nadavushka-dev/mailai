package store

// Persister is what the Store writes through to. Every method is called
// under the Store's lock and must be durable on return. nil means memory only.
type Persister interface {
	// LoadAll returns every stored message, by id ascending, with the
	// per-box eviction counts and the last id handed out.
	LoadAll() (msgs []Message, evicted map[Address]uint64, lastID uint64, err error)
	Insert(box Address, m *Message) error
	// Mark records new read_at and seen_at values.
	Mark(mid string, readAt, seenAt int64) error
	Evict(box Address, mid string, evicted uint64) error
}
