// Package tokens issues and checks agent tokens (SPEC §9): the proof for an
// agent with no pane, such as one started by hand or headless. The daemon
// keeps only hashes; the token itself is shown once, at issue.
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nadavushka-dev/mailai/internal/store"
)

// Prefix marks a mailai token so a stray one is recognisable in a log.
const Prefix = "mt_"

// Record is what the daemon keeps about one token.
type Record struct {
	Name      store.Address `json:"name"`
	Label     string        `json:"label,omitempty"`
	CreatedAt int64         `json:"created_at"`
	// Once says the token stops working after its first use... not in v1;
	// kept out of the format on purpose.
}

// Tokens is the set on disk.
type Tokens struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
	byH  map[string]Record // sha256 hex -> record
}

var (
	ErrBadName  = errors.New("a token name is [a-z][a-z0-9_-]{0,31}")
	ErrNotFound = errors.New("no such token")
)

// Open loads the token file, creating it on first write.
func Open(path string, now func() time.Time) (*Tokens, error) {
	if now == nil {
		now = time.Now
	}
	t := &Tokens{path: path, now: now, byH: map[string]Record{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &t.byH); err != nil {
		return nil, err
	}
	return t, nil
}

// ValidName says whether a token name is an acceptable principal name.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s[1:] {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// New issues a token for a principal and returns it; it is never stored.
func (t *Tokens) New(name store.Address, label string) (string, error) {
	if !ValidName(string(name)) {
		return "", ErrBadName
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := Prefix + hex.EncodeToString(raw)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byH[hash(tok)] = Record{Name: name, Label: label, CreatedAt: t.now().UnixNano()}
	return tok, t.save()
}

// Lookup returns the principal a token names.
func (t *Tokens) Lookup(tok string) (store.Address, bool) {
	if !strings.HasPrefix(tok, Prefix) {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.byH[hash(tok)]
	return r.Name, ok
}

// Has says a principal holds a token. Such a name is reserved: a Herdr
// agent that takes it is placed under its pane id (SPEC §9).
func (t *Tokens) Has(name store.Address) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.byH {
		if r.Name == name {
			return true
		}
	}
	return false
}

// Revoke removes every token of a principal; false when there was none.
func (t *Tokens) Revoke(name store.Address) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for h, r := range t.byH {
		if r.Name == name {
			delete(t.byH, h)
			n++
		}
	}
	if n == 0 {
		return 0, ErrNotFound
	}
	return n, t.save()
}

// List returns the records, by name then creation.
func (t *Tokens) List() []Record {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Record, 0, len(t.byH))
	for _, r := range t.byH {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

func (t *Tokens) save() error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t.byH, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}

func hash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}
