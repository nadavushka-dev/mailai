package harness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const existing = `{
  "model": "opus",
  "hooks": {
    "PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "my-own-hook.sh"}]}],
    "SessionStart": []
  }
}`

// D14/§8: install adds mailai's entries beside the user's, uninstall removes
// exactly those, and other settings are untouched.
func TestInstallUninstallLeaveOthersAlone(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(cfg), 0o700)
	os.WriteFile(cfg, []byte(existing), 0o600)
	ms, _ := Load("")
	m := ms["claude-code"]

	changed, err := m.Install(home, "/usr/local/bin/mailai")
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	b, _ := os.ReadFile(cfg)
	var root map[string]any
	json.Unmarshal(b, &root)
	if root["model"] != "opus" {
		t.Fatal("other settings lost")
	}
	hooks := root["hooks"].(map[string]any)
	post := toGroups(hooks["PostToolUse"])
	if len(post) != 2 || post[0].Hooks[0].Command != "my-own-hook.sh" || post[1].Hooks[0].Command != "/usr/local/bin/mailai hook claude-code PostToolUse" {
		t.Fatalf("%+v", post)
	}
	perm := toGroups(hooks["PermissionRequest"])
	if len(perm) != 1 || perm[0].Hooks[0].Timeout != 600 {
		t.Fatalf("%+v", perm)
	}
	st := m.Check(home, "/usr/local/bin/mailai")
	if !st.Installed || !st.Current || len(st.Missing) != 0 {
		t.Fatalf("%+v", st)
	}
	// Installing again changes nothing.
	if changed, _ := m.Install(home, "/usr/local/bin/mailai"); changed {
		t.Fatal("second install changed the file")
	}
	// A moved binary is stale, and install refreshes it without doubling.
	st = m.Check(home, "/opt/mailai")
	if st.Current || len(st.Stale) == 0 {
		t.Fatalf("%+v", st)
	}
	m.Install(home, "/opt/mailai")
	b, _ = os.ReadFile(cfg)
	if strings.Count(string(b), "hook claude-code PostToolUse") != 1 || !strings.Contains(string(b), "/opt/mailai hook") {
		t.Fatalf("%s", b)
	}
	changed, err = m.Uninstall(home)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	b, _ = os.ReadFile(cfg)
	if strings.Contains(string(b), "mailai") || !strings.Contains(string(b), "my-own-hook.sh") || !strings.Contains(string(b), `"model": "opus"`) {
		t.Fatalf("%s", b)
	}
	st = m.Check(home, "/opt/mailai")
	if st.Installed {
		t.Fatal("still installed after uninstall")
	}
	if changed, _ := m.Uninstall(home); changed {
		t.Fatal("second uninstall changed the file")
	}
}

func TestInstallCreatesMissingConfig(t *testing.T) {
	home := t.TempDir()
	ms, _ := Load("")
	m := ms["claude-code"]
	if _, err := m.Install(home, "mailai"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil || !strings.Contains(string(b), "mailai hook claude-code SessionStart") {
		t.Fatal(err, string(b))
	}
	if m.Check(home, "mailai").Current != true {
		t.Fatal("not current after fresh install")
	}
}

func TestInstallRefusesBrokenJSON(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(cfg), 0o700)
	os.WriteFile(cfg, []byte("{not json"), 0o600)
	ms, _ := Load("")
	if _, err := ms["claude-code"].Install(home, "mailai"); err == nil {
		t.Fatal("must not overwrite a file it cannot parse")
	}
}

func TestManualFormatExplainsInsteadOfEditing(t *testing.T) {
	ms, _ := Load("")
	_, err := ms["codex"].Install(t.TempDir(), "mailai")
	var me *ManualError
	if !errors.As(err, &me) || !strings.Contains(err.Error(), "Stop -> mailai hook codex Stop") {
		t.Fatalf("%v", err)
	}
}
