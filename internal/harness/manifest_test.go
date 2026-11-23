package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/adapter"
)

func TestBundledManifestsLoadAndUserReplaces(t *testing.T) {
	ms, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	cc := ms["claude-code"]
	if cc == nil || !cc.Bundled || cc.Events["Stop"].State != adapter.Done || !cc.Events["UserPromptSubmit"].Notice || cc.Approval == nil || cc.Approval.Event != "PermissionRequest" {
		t.Fatalf("%+v", cc)
	}
	if ms["codex"] == nil {
		t.Fatal("codex missing")
	}
	if cc.ConfigPath("/home/x") != "/home/x/.claude/settings.json" {
		t.Fatal(cc.ConfigPath("/home/x"))
	}
	if cc.Command("/usr/local/bin/mailai", "Stop") != "/usr/local/bin/mailai hook claude-code Stop" {
		t.Fatal(cc.Command("mailai", "Stop"))
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "codex.toml"), []byte(`name = "codex"
config = "~/other.json"
format = "claude-settings-json"
[events.Stop]
state = "done"
`), 0o600)
	os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(`name = "mine"
config = "~/mine.json"
format = "claude-settings-json"
[events.Done]
state = "done"
`), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600)
	ms, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ms["codex"].Bundled || ms["codex"].Config != "~/other.json" || ms["codex"].Path == "" {
		t.Fatalf("user manifest did not replace bundled: %+v", ms["codex"])
	}
	if ms["mine"] == nil || Names(ms)[0] != "claude-code" {
		t.Fatal(Names(ms))
	}
}

func TestManifestValidation(t *testing.T) {
	if _, err := parse([]byte(`name = "x"`)); err == nil {
		t.Fatal("missing fields")
	}
	if _, err := parse([]byte("name = \"x\"\nconfig = \"c\"\nformat = \"f\"\n[events.E]\nstate = \"sleepy\"\n")); err == nil || !strings.Contains(err.Error(), "sleepy") {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.toml"), []byte("name = [\n"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Fatal("bad toml must fail loud")
	}
}

// §7a: a blocking ask killed by the harness's own tool timeout looks like
// the person answering in the session, which closes the Inbox item. The
// skill tells agents to time out first and resume.
func TestSkillTellsAgentsToOutwaitTheHarnessTimeout(t *testing.T) {
	for _, want := range []string{"--timeout", "--wait-for", "--attach", "Show the person a file", "mailai wait", "Do not poll", "mailai withdraw", "--replaces", "never kill"} {
		if !strings.Contains(Skill, want) {
			t.Fatalf("skill lacks %q", want)
		}
	}
}

func TestSkillIsShortAndNamesTheCommands(t *testing.T) {
	if len(Skill) > 2500 {
		t.Fatalf("skill is %d bytes; keep it short (D12)", len(Skill))
	}
	for _, want := range []string{"mailai read --unread", "mailai ask-human", "mailai ask ", "verified human"} {
		if !strings.Contains(Skill, want) {
			t.Fatalf("skill lacks %q", want)
		}
	}
}
