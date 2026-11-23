package harness

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Add-ons (SPEC §7, decided 2026-11-23): a harness that can hand its agent
// a message gets a small add-on that runs `mailai listen` and passes each
// wake line on, so nothing is typed into the agent's pane. The add-on only
// carries lines; every mail rule stays in the daemon.

//go:embed addons/pi/index.ts
var addonFS embed.FS

// addons maps a harness to its add-on's files under addons/.
var addons = map[string][]string{
	"pi": {"addons/pi/index.ts"},
}

// AddonHarnesses are the harnesses that have an add-on.
func AddonHarnesses() []string {
	var out []string
	for h := range addons {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// WriteAddon writes harness's add-on into dir, with the mailai skill beside
// it as SKILL.md. Idempotent: the add-on's files are rewritten; a SKILL.md
// mailai did not write is left alone (and reported).
func WriteAddon(harness, dir string) (SkillState, error) {
	files, ok := addons[harness]
	if !ok {
		return "", fmt.Errorf("no add-on for %q; there is one for %v", harness, AddonHarnesses())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, f := range files {
		b, err := addonFS.ReadFile(f)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644); err != nil {
			return "", err
		}
	}
	return InstallSkill(filepath.Join(dir, "SKILL.md"))
}
