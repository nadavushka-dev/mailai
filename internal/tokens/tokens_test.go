package tokens

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/store"
)

func TestIssueLookupRevokePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	tk, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := tk.New("reviewer", "the review helper")
	if err != nil || !strings.HasPrefix(tok, Prefix) || len(tok) != len(Prefix)+48 {
		t.Fatal(tok, err)
	}
	if name, ok := tk.Lookup(tok); !ok || name != "reviewer" {
		t.Fatal(name, ok)
	}
	if _, ok := tk.Lookup(tok + "x"); ok {
		t.Fatal("near miss accepted")
	}
	if _, ok := tk.Lookup("nope"); ok {
		t.Fatal("non-token accepted")
	}
	// The file holds hashes, never the token.
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), tok[len(Prefix):]) {
		t.Fatal("token stored in clear")
	}
	tk2, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := tk2.Lookup(tok); !ok || name != "reviewer" {
		t.Fatal("not persisted")
	}
	tk2.New("reviewer", "second")
	if l := tk2.List(); len(l) != 2 || l[0].Name != "reviewer" || l[1].Label != "second" {
		t.Fatalf("%+v", l)
	}
	n, err := tk2.Revoke("reviewer")
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, ok := tk2.Lookup(tok); ok {
		t.Fatal("revoked token still works")
	}
	if _, err := tk2.Revoke("reviewer"); err != ErrNotFound {
		t.Fatal(err)
	}
}

// Has says a name holds a token: such a name is reserved (SPEC §9).
func TestHas(t *testing.T) {
	tk, _ := Open(filepath.Join(t.TempDir(), "t.json"), nil)
	if tk.Has("builder") {
		t.Fatal("no token yet")
	}
	tk.New("builder", "")
	if !tk.Has("builder") || tk.Has("build") {
		t.Fatal("Has")
	}
	tk.Revoke("builder")
	if tk.Has("builder") {
		t.Fatal("revoked")
	}
}

func storeAddr(s string) store.Address { return store.Address(s) }

func TestNames(t *testing.T) {
	tk, _ := Open(filepath.Join(t.TempDir(), "t.json"), nil)
	for _, bad := range []string{"", "Human", "1abc", "a b", "a/b", strings.Repeat("a", 33), "human@devbox"} {
		if _, err := tk.New(storeAddr(bad), ""); err != ErrBadName {
			t.Fatalf("%q accepted", bad)
		}
	}
	for _, good := range []string{"a", "reviewer", "w1-p3", "sub_agent2"} {
		if _, err := tk.New(storeAddr(good), ""); err != nil {
			t.Fatalf("%q refused: %v", good, err)
		}
	}
}
