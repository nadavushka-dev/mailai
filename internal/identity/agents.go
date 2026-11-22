package identity

import (
	"path/filepath"
	"strings"
)

// agentPrograms are the coding-agent CLIs mailai recognises by name. A
// program not listed here, run outside every pane with no token, still
// passes as the person: the known limit (SPEC §9).
var agentPrograms = map[string]bool{
	"claude": true, "codex": true, "pi": true, "gemini": true, "opencode": true,
	"aider": true, "goose": true, "amp": true, "crush": true, "cursor-agent": true,
	"qwen": true, "droid": true, "copilot": true, "kiro-cli": true,
}

// agentPackages are path parts of agents installed as scripts an
// interpreter runs (node .../claude-code/cli.js).
var agentPackages = []string{"/claude-code/", "/pi-coding-agent/", "/@openai/codex/", "/gemini-cli/", "/opencode-ai/"}

var interpreters = map[string]bool{"node": true, "bun": true, "deno": true, "python": true, "python3": true, "tsx": true}

// IsAgentProgram says whether argv is a coding agent, and which.
func IsAgentProgram(argv []string) (string, bool) {
	if len(argv) == 0 {
		return "", false
	}
	if name := programName(argv[0]); agentPrograms[name] {
		return name, true
	}
	if !interpreters[programName(argv[0])] {
		return "", false
	}
	// The script an interpreter runs: the first argument that is not a flag.
	for _, a := range argv[1:] {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if name := programName(a); agentPrograms[name] {
			return name, true
		}
		for _, pkg := range agentPackages {
			if strings.Contains(filepath.ToSlash(a), pkg) {
				return strings.Trim(pkg, "/@"), true
			}
		}
		return "", false
	}
	return "", false
}

// programName is argv[0]'s base name: no directory, no login-shell dash, no
// script extension.
func programName(arg0 string) string {
	name := strings.TrimPrefix(filepath.Base(arg0), "-")
	for _, ext := range []string{".js", ".mjs", ".cjs", ".py", ".ts"} {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}
