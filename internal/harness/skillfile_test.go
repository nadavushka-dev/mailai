package harness

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// §8: integration install delivers the guide as an Agent Skill, a mailai/
// directory holding SKILL.md: frontmatter (name matching the directory, a
// description that routes an agent to it) then the guide itself.
func TestSkillFileIsAnAgentSkill(t *testing.T) {
	f := SkillFile()
	if !strings.HasPrefix(f, "---\n") {
		t.Fatalf("no frontmatter: %q", f[:40])
	}
	front, body, ok := strings.Cut(f[4:], "\n---\n")
	if !ok {
		t.Fatal("frontmatter not closed")
	}
	fields := map[string]string{}
	for _, l := range strings.Split(front, "\n") {
		k, v, ok := strings.Cut(l, ": ")
		if !ok {
			t.Fatalf("frontmatter line %q is not key: value", l)
		}
		fields[k] = v
	}
	if len(fields) != 2 || fields["name"] != SkillName || SkillName != "mailai" {
		t.Fatalf("%v", fields)
	}
	// The Agent Skills rules for a name, and the directory matches it.
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(fields["name"]) || len(fields["name"]) > 64 {
		t.Fatal(fields["name"])
	}
	d := fields["description"]
	if d == "" || len(d) > 1024 {
		t.Fatalf("description %q", d)
	}
	// A plain YAML scalar: no ": ", no " #", no leading indicator, no quotes.
	if strings.Contains(d, ": ") || strings.Contains(d, " #") || strings.ContainsAny(d[:1], "-?:,[]{}#&*!|>'\"%@`") {
		t.Fatalf("description is not a plain YAML scalar: %q", d)
	}
	for _, want := range []string{"mail", "ask the person or another agent", "wait for a reply", "show the person a file"} {
		if !strings.Contains(d, want) {
			t.Fatalf("description lacks %q: %q", want, d)
		}
	}
	// The body is the marker line, then the guide exactly as --skill prints it.
	if !strings.HasPrefix(body, skillMarker) || !strings.HasSuffix(body, "\n\n"+Skill) {
		t.Fatalf("%q", body)
	}
	if strings.HasPrefix(Skill, "---") || strings.Contains(Skill, skillMarker) {
		t.Fatal("the bare guide must carry no frontmatter or marker")
	}
}

func TestManifestSkillPaths(t *testing.T) {
	ms, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	home := "/h"
	if p := ms["claude-code"].SkillPath(home); p != filepath.Join(home, ".claude", "skills", "mailai", "SKILL.md") {
		t.Fatal(p)
	}
	if p := ms["codex"].SkillPath(home); p != filepath.Join(home, ".agents", "skills", "mailai", "SKILL.md") {
		t.Fatal(p)
	}
	m, err := parse([]byte("name = \"x\"\nconfig = \"c\"\nformat = \"manual\"\n"))
	if err != nil || m.SkillPath(home) != "" {
		t.Fatalf("%v %q", err, m.SkillPath(home))
	}
}

// Install is idempotent, rewrites a file an older mailai wrote, and
// uninstall removes only a file mailai wrote.
func TestSkillInstallUninstallCheck(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".claude", "skills", "mailai", "SKILL.md")
	if st := CheckSkill(p); st != SkillAbsent {
		t.Fatal(st)
	}
	r, err := InstallSkill(p)
	if err != nil || r != SkillWritten {
		t.Fatal(r, err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != SkillFile() {
		t.Fatalf("%s", b)
	}
	if st := CheckSkill(p); st != SkillCurrent {
		t.Fatal(st)
	}
	if r, err := InstallSkill(p); err != nil || r != SkillCurrent {
		t.Fatal(r, err)
	}
	// What an older mailai wrote: our marker, other text.
	os.WriteFile(p, []byte("---\nname: mailai\ndescription: old\n---\n"+skillMarker+"\n\nold guide\n"), 0o644)
	if st := CheckSkill(p); st != SkillStale {
		t.Fatal(st)
	}
	if r, err := InstallSkill(p); err != nil || r != SkillUpdated {
		t.Fatal(r, err)
	}
	if b, _ := os.ReadFile(p); string(b) != SkillFile() {
		t.Fatalf("%s", b)
	}
	r, err = UninstallSkill(p)
	if err != nil || r != SkillRemoved {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Fatal("empty mailai/ left behind", err)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(p))); err != nil {
		t.Fatal("the skills directory is not ours to remove", err)
	}
	if r, err := UninstallSkill(p); err != nil || r != SkillAbsent {
		t.Fatal(r, err)
	}
}

// A mailai skill the user wrote is theirs: install does not overwrite it,
// uninstall does not delete it, status says so.
func TestSkillLeavesAUserSkillAlone(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "skills", "mailai", "SKILL.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	mine := "---\nname: mailai\ndescription: my own notes\n---\nmine\n"
	os.WriteFile(p, []byte(mine), 0o644)
	if st := CheckSkill(p); st != SkillForeign {
		t.Fatal(st)
	}
	if r, err := InstallSkill(p); err != nil || r != SkillForeign {
		t.Fatal(r, err)
	}
	if r, err := UninstallSkill(p); err != nil || r != SkillForeign {
		t.Fatal(r, err)
	}
	if b, _ := os.ReadFile(p); string(b) != mine {
		t.Fatalf("%s", b)
	}
	// Uninstall keeps a mailai/ that holds more than our SKILL.md.
	InstallSkill(filepath.Join(dir, "other", "mailai", "SKILL.md"))
	extra := filepath.Join(dir, "other", "mailai", "notes.md")
	os.WriteFile(extra, []byte("x"), 0o644)
	if r, _ := UninstallSkill(filepath.Join(dir, "other", "mailai", "SKILL.md")); r != SkillRemoved {
		t.Fatal(r)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatal(err)
	}
}
