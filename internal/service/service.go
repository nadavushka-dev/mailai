// Package service keeps the daemon running: a per-user launchd agent on
// macOS, a systemd user unit on Linux, each running `mailai daemon run`
// at login and again after a crash (SPEC §4).
//
// Adapted from enigma/internal/service (MIT, same author): copied, never
// imported. The background fallback and linger are left out.
package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	// Label is the launchd job label.
	Label = "dev.mailai.daemon"
	// UnitName is the systemd user unit.
	UnitName = "mailai.service"
)

// envKeys are the variables that place the daemon's files and socket
// (config.DefaultPaths, Paths.Socket); set at install, they go into the
// service so the daemon it starts is the one this shell talks to.
// PATH is carried because launchd/systemd start the daemon with a minimal
// one, and the herdr adapter runs `herdr` from wherever the person has it.
var envKeys = []string{"MAILAI_HOME", "MAILAI_SOCKET", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "PATH"}

// Runner executes service-manager commands (launchctl, systemctl --user)
// and returns their combined output.
type Runner interface {
	Run(name string, args ...string) (string, error)
}

// ExecRunner runs commands for real.
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Options say where and how the service is installed.
type Options struct {
	GOOS          string
	Home          string
	XDGConfigHome string // where the systemd user unit goes; ~/.config when empty
	UID           int    // launchd's gui/<uid> domain
	Bin           string // absolute path of the mailai binary
	LogPath       string // the daemon's stdout and stderr: the data dir's daemon.log
	Env           map[string]string
	Runner        Runner
}

// Defaults fills Options for this machine and this binary.
func Defaults(logPath string) (Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		return Options{}, err
	}
	if bin, err = filepath.Abs(bin); err != nil {
		return Options{}, err
	}
	env := map[string]string{}
	for _, k := range envKeys {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return Options{GOOS: runtime.GOOS, Home: home, XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"), UID: os.Getuid(),
		Bin: bin, LogPath: logPath, Env: env, Runner: ExecRunner{}}, nil
}

// File is where the service definition lives.
func File(o Options) string {
	if o.GOOS == "darwin" {
		return filepath.Join(o.Home, "Library", "LaunchAgents", Label+".plist")
	}
	cfg := o.XDGConfigHome
	if cfg == "" {
		cfg = filepath.Join(o.Home, ".config")
	}
	return filepath.Join(cfg, "systemd", "user", UnitName)
}

// Manager names the service manager for o.GOOS: launchd, systemd or "".
func Manager(o Options) string {
	switch o.GOOS {
	case "darwin":
		return "launchd"
	case "linux":
		return "systemd"
	}
	return ""
}

func unsupported(o Options) error {
	return fmt.Errorf("no service manager support on %s; run `mailai daemon run` under your own supervisor", o.GOOS)
}

func (o Options) runner() Runner {
	if o.Runner == nil {
		return ExecRunner{}
	}
	return o.Runner
}

func (o Options) domain() string { return "gui/" + strconv.Itoa(o.UID) }

// Install writes the service definition and starts it. Installing again
// replaces the definition and restarts the daemon.
func Install(o Options) error {
	if Manager(o) == "" {
		return unsupported(o)
	}
	run := o.runner()
	if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o700); err != nil {
		return err
	}
	p := File(o)
	switch o.GOOS {
	case "darwin":
		_, _ = run.Run("launchctl", "bootout", o.domain(), p) // not loaded yet is fine
		if err := writeFile(p, Plist(o)); err != nil {
			return err
		}
		_, err := run.Run("launchctl", "bootstrap", o.domain(), p)
		return err
	default: // linux
		prev, hadPrev := os.ReadFile(p)
		if err := writeFile(p, Unit(o)); err != nil {
			return err
		}
		for _, args := range [][]string{{"daemon-reload"}, {"enable", UnitName}, {"restart", UnitName}} {
			if _, err := run.Run("systemctl", append([]string{"--user"}, args...)...); err != nil {
				// Leave things as they were: no half-installed unit.
				if hadPrev == nil {
					writeFile(p, string(prev))
				} else {
					removeIfExists(p)
				}
				return fmt.Errorf("%w (no usable systemd user session? then run `mailai daemon run` under your own supervisor)", err)
			}
		}
		return nil
	}
}

// Uninstall stops the daemon and removes the service definition. Removing
// what is not installed is fine.
func Uninstall(o Options) error {
	if Manager(o) == "" {
		return unsupported(o)
	}
	run := o.runner()
	p := File(o)
	switch o.GOOS {
	case "darwin":
		_, _ = run.Run("launchctl", "bootout", o.domain(), p) // not loaded is fine
		return removeIfExists(p)
	default: // linux
		installed := fileExists(p)
		_, derr := run.Run("systemctl", "--user", "disable", "--now", UnitName) // not enabled is fine
		if err := removeIfExists(p); err != nil {
			return err
		}
		// When systemd cannot even be asked to disable, there is nothing
		// running to stop and nothing to reload: removing the unit is all.
		if _, err := run.Run("systemctl", "--user", "daemon-reload"); err != nil && installed && derr == nil {
			return err
		}
		return nil
	}
}

// StatusInfo is what `mailai service status` reports.
type StatusInfo struct {
	Manager   string `json:"manager"`
	Path      string `json:"path"`
	Log       string `json:"log"`
	Installed bool   `json:"installed"` // the definition file exists
	Loaded    bool   `json:"loaded"`    // launchd: bootstrapped; systemd: enabled
	Running   bool   `json:"running"`   // the daemon process is up
}

// Status asks the service manager about the daemon.
func Status(o Options) (StatusInfo, error) {
	if Manager(o) == "" {
		return StatusInfo{}, unsupported(o)
	}
	run := o.runner()
	s := StatusInfo{Manager: Manager(o), Path: File(o), Log: o.LogPath, Installed: fileExists(File(o))}
	switch o.GOOS {
	case "darwin":
		out, err := run.Run("launchctl", "print", o.domain()+"/"+Label)
		s.Loaded = err == nil
		s.Running = s.Loaded && strings.Contains(out, "state = running")
	default: // linux
		_, err := run.Run("systemctl", "--user", "is-enabled", UnitName)
		s.Loaded = err == nil
		_, err = run.Run("systemctl", "--user", "is-active", UnitName)
		s.Running = err == nil
	}
	return s, nil
}

// Plist is the launchd agent: start at login, restart whenever it exits.
func Plist(o Options) string {
	var env strings.Builder
	if len(o.Env) > 0 {
		env.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, k := range sortedKeys(o.Env) {
			fmt.Fprintf(&env, "\t\t<key>%s</key><string>%s</string>\n", xmlEscape(k), xmlEscape(o.Env[k]))
		}
		env.WriteString("\t</dict>\n")
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>daemon</string><string>run</string></array>
%s	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>10</integer>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, xmlEscape(o.Bin), env.String(), xmlEscape(o.LogPath), xmlEscape(o.LogPath))
}

// Unit is the systemd user unit: start at login, restart after a crash.
func Unit(o Options) string {
	var env strings.Builder
	for _, k := range sortedKeys(o.Env) {
		fmt.Fprintf(&env, "Environment=%s\n", unitEscape(strconv.Quote(k+"="+o.Env[k])))
	}
	return fmt.Sprintf(`[Unit]
Description=mailai daemon

[Service]
ExecStart=%s daemon run
%sRestart=on-failure
RestartSec=5
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, unitEscape(strconv.Quote(o.Bin)), env.String(), unitEscape(o.LogPath), unitEscape(o.LogPath))
}

// unitEscape doubles %, which systemd would otherwise expand as a specifier.
func unitEscape(s string) string { return strings.ReplaceAll(s, "%", "%%") }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func writeFile(p, content string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
