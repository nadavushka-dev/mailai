package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// [mail] keep_days (SPEC §6): read mail goes after 7 days by default; 0
// keeps it until evicted; a negative number is a mistake.
func TestKeepDays(t *testing.T) {
	if got := (Config{}).KeepFor(); got != 7*24*time.Hour {
		t.Fatalf("default %s", got)
	}
	f := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(f, []byte("[mail]\nkeep_days = 30\n"), 0o600)
	if c, err := Load(f); err != nil || c.KeepFor() != 30*24*time.Hour {
		t.Fatal(err, c.KeepFor())
	}
	os.WriteFile(f, []byte("[mail]\nkeep_days = 0\n"), 0o600)
	if c, err := Load(f); err != nil || c.KeepFor() != 0 {
		t.Fatal(err, c.KeepFor())
	}
	os.WriteFile(f, []byte("[mail]\nkeep_days = -1\n"), 0o600)
	if _, err := Load(f); err == nil {
		t.Fatal("a negative keep_days must be an error")
	}
}

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
	if err != nil || !c.Wake() || !c.RequireTerminal() {
		t.Fatal(err, c)
	}
	f := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(f, []byte("adapter = \"herdr\"\n[mail]\nwake = false\n"), 0o600)
	c, err = Load(f)
	if err != nil || c.Adapter != "herdr" || c.Wake() {
		t.Fatal(err, c)
	}
	os.WriteFile(f, []byte("[fleet]\nname = \"s2\"\ninbox = \"dev@s1\"\n"), 0o600)
	c, err = Load(f)
	if err != nil || c.MachineName() != "s2" || c.Fleet.Inbox != "dev@s1" {
		t.Fatal(err, c)
	}
	os.WriteFile(f, []byte("[identity]\nrequire_terminal = false\n"), 0o600)
	if c, _ = Load(f); c.RequireTerminal() {
		t.Fatal("require_terminal = false must turn it off")
	}
	// [web]: served from the start, reached at url, logins last session.
	os.WriteFile(f, []byte("[web]\nport = 8473\nurl = \"http://box.ts.net:8474\"\nsession = \"168h\"\n"), 0o600)
	if c, err = Load(f); err != nil || c.Web.Port != 8473 || c.Web.URL != "http://box.ts.net:8474" || c.WebSession() != 168*time.Hour {
		t.Fatal(err, c.Web)
	}
	if (Config{}).WebSession() != 12*time.Hour {
		t.Fatal("logins last 12 h by default")
	}
	os.WriteFile(f, []byte("[web]\nsession = \"soon\"\n"), 0o600)
	if _, err = Load(f); err == nil {
		t.Fatal("a bad [web] session must be an error")
	}
	// [addon] agents: who the add-on is switched on for; nobody by default.
	if a := (Config{}).AddonAgents(); a == nil || len(a) != 0 {
		t.Fatalf("nobody by default, as an empty list: %#v", a)
	}
	os.WriteFile(f, []byte("[addon]\nagents = [\"conductor\", \"lane-*\"]\n"), 0o600)
	if c, err = Load(f); err != nil || len(c.AddonAgents()) != 2 || c.AddonAgents()[1] != "lane-*" {
		t.Fatal(err, c.AddonAgents())
	}
	os.WriteFile(f, []byte("[addon]\nagents = [\"lane-[\"]\n"), 0o600)
	if _, err = Load(f); err == nil {
		t.Fatal("a bad [addon] pattern must be an error")
	}
	// Test rigs run under whatever started them, an agent included.
	if !(Config{}).DetectAgents() {
		t.Fatal("agent detection is on by default")
	}
	os.WriteFile(f, []byte("[identity]\ndetect_agents = false\n"), 0o600)
	if c, _ = Load(f); c.DetectAgents() {
		t.Fatal("detect_agents = false must turn it off")
	}
	if (Config{}).MachineName() == "" {
		t.Fatal("default machine name is empty")
	}
	if !(Config{}).NotifyEnabled() {
		t.Fatal("[notify] is on by default")
	}
	os.WriteFile(f, []byte("[notify]\nenabled = false\ncommand = [\"ntfy\", \"publish\", \"mytopic\"]\n"), 0o600)
	if c, err = Load(f); err != nil || c.NotifyEnabled() || len(c.Notify.Command) != 3 || c.Notify.Command[2] != "mytopic" {
		t.Fatal(err, c)
	}
	os.WriteFile(f, []byte("adapter = [\n"), 0o600)
	if _, err := Load(f); err == nil {
		t.Fatal("bad toml must error")
	}
}
