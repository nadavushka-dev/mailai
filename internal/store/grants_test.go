package store

import (
	"path/filepath"
	"testing"
)

func sessions(m map[Address]string) SessionOf { return func(a Address) string { return m[a] } }

func TestRightsAndCovers(t *testing.T) {
	r, err := ParseRights("respond, read")
	if err != nil || len(r) != 2 || r[0] != RightRead || r[1] != RightRespond {
		t.Fatal(r, err)
	}
	if _, err := ParseRights("root"); err != ErrBadRight {
		t.Fatal(err)
	}
	if _, err := ParseRights(""); err != ErrBadRight {
		t.Fatal(err)
	}
	so := sessions(map[Address]string{"w1": "api", "w2": "api", "w3": "web"})
	if !Covers("all", "anyone", so) || !Covers("session:api", "w1", so) || Covers("session:api", "w3", so) || Covers("session:api", "ghost", so) {
		t.Fatal("session scope")
	}
	if !Covers("w1", "w1", so) || Covers("w1", "w2", so) {
		t.Fatal("name scope")
	}
}

// SPEC §10: delegation is narrower only: a subset of rights over a subset
// of the scope.
func TestNarrower(t *testing.T) {
	so := sessions(map[Address]string{"w1": "api", "w3": "web"})
	all := Grant{Rights: []Right{RightRead, RightRespond}, Scope: "all"}
	api := Grant{Rights: []Right{RightRead, RightRespond}, Scope: "session:api"}
	cases := []struct {
		child, parent Grant
		want          bool
	}{
		{Grant{Rights: []Right{RightRespond}, Scope: "session:api"}, all, true},
		{Grant{Rights: []Right{RightAdmin}, Scope: "w1"}, all, false},
		{Grant{Rights: []Right{RightRespond}, Scope: "w1"}, api, true},
		{Grant{Rights: []Right{RightRespond}, Scope: "w3"}, api, false},
		{Grant{Rights: []Right{RightRespond}, Scope: "all"}, api, false},
		{Grant{Rights: []Right{RightRespond}, Scope: "session:web"}, api, false},
		{Grant{Rights: []Right{RightRead, RightRespond}, Scope: "session:api"}, api, true},
		{Grant{Rights: []Right{RightRead}, Scope: "w1"}, Grant{Rights: []Right{RightRead}, Scope: "w1"}, true},
		{Grant{Rights: []Right{RightRead}, Scope: "w2"}, Grant{Rights: []Right{RightRead}, Scope: "w1"}, false},
	}
	for i, c := range cases {
		if got := Narrower(c.child, c.parent, so); got != c.want {
			t.Fatalf("case %d: got %v", i, got)
		}
	}
}

func TestGrantsStoreCascadeAndPersist(t *testing.T) {
	dir := t.TempDir()
	db := openDB(t, dir)
	s, _ := Load(Options{}, db)
	g1, err := s.AddGrant(Grant{Principal: "sup", Rights: []Right{RightRespond}, Scope: "session:api", GrantedBy: Human})
	if err != nil || g1.ID != 1 {
		t.Fatal(g1, err)
	}
	g2, _ := s.AddGrant(Grant{Principal: "sub", Rights: []Right{RightRespond}, Scope: "w1", GrantedBy: "sup", Parent: g1.ID})
	g3, _ := s.AddGrant(Grant{Principal: "other", Rights: []Right{RightRead}, Scope: "all", GrantedBy: Human})
	so := sessions(map[Address]string{"w1": "api"})
	if g, ok := s.Allowed("sup", RightRespond, "w1", so); !ok || g.ID != g1.ID {
		t.Fatal("sup should respond over w1")
	}
	if _, ok := s.Allowed("sup", RightAdmin, "w1", so); ok {
		t.Fatal("no admin")
	}
	if _, ok := s.Allowed("sub", RightRespond, "w2", so); ok {
		t.Fatal("sub is scoped to w1")
	}
	db.Close()
	db2, _ := OpenSQLite(filepath.Join(dir, "store.db"))
	defer db2.Close()
	s2, _ := Load(Options{}, db2)
	if len(s2.Grants()) != 3 {
		t.Fatalf("%+v", s2.Grants())
	}
	n, err := s2.RemoveGrant(g1.ID)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, ok := s2.Grant(g2.ID); ok {
		t.Fatal("delegated grant survived its parent")
	}
	if _, ok := s2.Grant(g3.ID); !ok {
		t.Fatal("unrelated grant removed")
	}
	if _, err := s2.RemoveGrant(99); err != ErrNoGrant {
		t.Fatal(err)
	}
	g4, _ := s2.AddGrant(Grant{Principal: "x", Rights: []Right{RightRead}, Scope: "all"})
	if g4.ID != 4 {
		t.Fatalf("id counter: %d", g4.ID)
	}
}
