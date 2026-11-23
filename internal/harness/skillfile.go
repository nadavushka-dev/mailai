package harness

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The guide is delivered as an Agent Skill (agentskills.io): a directory
// named for the skill holding SKILL.md, YAML frontmatter then the
// instructions. A harness lists the name and description to its agent and
// loads the rest only when the agent wants it, so the description is all
// that costs context up front (D12: one line, and ours, not a sender's).

// SkillName is the skill's name and its directory's.
const SkillName = "mailai"

// skillDescription routes an agent to the skill. A plain YAML scalar: no
// ": " and no " #".
const skillDescription = "Mail between coding agents and the person who runs them. Use when you have mail, need to ask the person or another agent something, wait for a reply, or show the person a file."

// skillMarkerTag starts the line that marks a SKILL.md as mailai's, so
// uninstall removes only files mailai wrote. Keep it stable across
// versions: an upgrade recognises older files by it.
const skillMarkerTag = "<!-- mailai:generated"

const skillMarker = skillMarkerTag + " by `mailai integration install`, which rewrites this file on upgrade; `mailai integration uninstall` removes it. -->"

// SkillFile is the SKILL.md integration install writes: frontmatter, the
// marker line, then Skill exactly as `mailai --skill` prints it.
func SkillFile() string {
	return "---\nname: " + SkillName + "\ndescription: " + skillDescription + "\n---\n" + skillMarker + "\n\n" + Skill
}

// SkillState is what is at a skill path, or what install or uninstall did.
type SkillState string

const (
	SkillAbsent  SkillState = "absent"  // nothing there
	SkillCurrent SkillState = "current" // ours, as this mailai writes it
	SkillStale   SkillState = "stale"   // ours, written by another mailai version
	SkillForeign SkillState = "foreign" // a SKILL.md mailai did not write; never touched
	SkillWritten SkillState = "written" // install created it
	SkillUpdated SkillState = "updated" // install rewrote a stale one
	SkillRemoved SkillState = "removed" // uninstall deleted it
)

// SkillPath is where the harness reads this skill, or "" when its manifest
// names no skills directory.
func (m *Manifest) SkillPath(home string) string {
	if m.Skills == "" {
		return ""
	}
	return filepath.Join(expandHome(home, m.Skills), SkillName, "SKILL.md")
}

func isOurSkill(b []byte) bool {
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, skillMarkerTag) {
			return true
		}
	}
	return false
}

// CheckSkill says what is at path.
func CheckSkill(path string) SkillState {
	b, err := os.ReadFile(path)
	switch {
	case err != nil:
		return SkillAbsent
	case string(b) == SkillFile():
		return SkillCurrent
	case isOurSkill(b):
		return SkillStale
	}
	return SkillForeign
}

// InstallSkill writes SKILL.md at path unless it is current already or is
// not ours.
func InstallSkill(path string) (SkillState, error) {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	res := SkillWritten
	if err == nil {
		switch {
		case string(b) == SkillFile():
			return SkillCurrent, nil
		case !isOurSkill(b):
			return SkillForeign, nil
		}
		res = SkillUpdated
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp := path + ".mailai-tmp"
	if err := os.WriteFile(tmp, []byte(SkillFile()), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return res, nil
}

// UninstallSkill deletes SKILL.md at path when mailai wrote it, and its
// directory when that is left empty.
func UninstallSkill(path string) (SkillState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SkillAbsent, nil
	}
	if err != nil {
		return "", err
	}
	if !isOurSkill(b) {
		return SkillForeign, nil
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	os.Remove(filepath.Dir(path)) // fails, as it should, when not empty
	return SkillRemoved, nil
}
