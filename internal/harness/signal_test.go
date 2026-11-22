package harness

import (
	"strings"
	"testing"
)

// SPEC §7a, D14: which harness events mean "new prompt", "a tool ran" and
// "the turn ended" is the manifest's to say, so the core knows no harness.
func TestManifestSignals(t *testing.T) {
	ms, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-code", "codex"} {
		m := ms[name]
		if m.Events["UserPromptSubmit"].Signal != "prompt" || m.Events["PostToolUse"].Signal != "tool_ran" || m.Events["Stop"].Signal != "turn_ended" {
			t.Fatalf("%s: %+v", name, m.Events)
		}
		if m.Events["SessionStart"].Signal != "" {
			t.Fatalf("%s: %+v", name, m.Events["SessionStart"])
		}
	}
	if _, err := parse([]byte("name = \"x\"\nconfig = \"c\"\nformat = \"f\"\n[events.E]\nstate = \"working\"\nsignal = \"guess\"\n")); err == nil || !strings.Contains(err.Error(), "guess") {
		t.Fatal(err)
	}
	// A signal rides on a state report, so it needs a state.
	if _, err := parse([]byte("name = \"x\"\nconfig = \"c\"\nformat = \"f\"\n[events.E]\nsignal = \"prompt\"\n")); err == nil {
		t.Fatal("signal without state accepted")
	}
}
