package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathsFromMailaiHomeAndSocketOverride(t *testing.T) {
	t.Setenv("MAILAI_HOME", "/x")
	t.Setenv("MAILAI_SOCKET", "")
	p, err := DefaultPaths()
	if err != nil || p.Data != "/x/data" || p.Config != "/x/config" {
		t.Fatalf("%+v %v", p, err)
	}
	if p.Socket() != "/x/data/mailai.sock" || p.StoreDB() != "/x/data/store.db" {
		t.Fatalf("%s %s", p.Socket(), p.StoreDB())
	}
	t.Setenv("MAILAI_SOCKET", "/tmp/s")
	if p.Socket() != "/tmp/s" {
		t.Fatal(p.Socket())
	}
}

func TestLoadMissingIsDefaultAndWakeParses(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil || !c.Wake() {
		t.Fatal(err, c)
	}
	f := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(f, []byte("adapter = \"herdr\"\n[mail]\nwake = false\n"), 0o600)
	c, err = Load(f)
	if err != nil || c.Adapter != "herdr" || c.Wake() {
		t.Fatal(err, c)
	}
	os.WriteFile(f, []byte("adapter = [\n"), 0o600)
	if _, err := Load(f); err == nil {
		t.Fatal("bad toml must error")
	}
}
