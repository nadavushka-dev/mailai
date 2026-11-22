package service

// Adapted from enigma/internal/service (MIT, same author).

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner records every service-manager command; nothing real runs.
type fakeRunner struct {
	calls [][]string
	fail  map[string]bool   // the joined command line -> fails
	out   map[string]string // the joined command line -> output
}

func (f *fakeRunner) Run(name string, args ...string) (string, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	line := strings.Join(call, " ")
	if f.fail[line] {
		return f.out[line], errors.New(name + " failed")
	}
	return f.out[line], nil
}

func opts(t *testing.T, goos string) (Options, *fakeRunner) {
	t.Helper()
	home := t.TempDir()
	r := &fakeRunner{}
	return Options{GOOS: goos, Home: home, UID: 501, Bin: "/opt/mail & co/mailai",
		LogPath: filepath.Join(home, "data", "mailai", "daemon.log"),
		Env:     map[string]string{"MAILAI_HOME": "/tmp/mh", "MAILAI_SOCKET": "/tmp/mh/s.sock"}, Runner: r}, r
}

func TestFilePaths(t *testing.T) {
	o, _ := opts(t, "darwin")
	if got, want := File(o), filepath.Join(o.Home, "Library/LaunchAgents/dev.mailai.daemon.plist"); got != want {
		t.Errorf("darwin: %s, want %s", got, want)
	}
	o, _ = opts(t, "linux")
	if got, want := File(o), filepath.Join(o.Home, ".config/systemd/user/mailai.service"); got != want {
		t.Errorf("linux: %s, want %s", got, want)
	}
	o.XDGConfigHome = filepath.Join(o.Home, "xdg")
	if got, want := File(o), filepath.Join(o.Home, "xdg/systemd/user/mailai.service"); got != want {
		t.Errorf("linux with XDG_CONFIG_HOME: %s, want %s", got, want)
	}
}

func TestPlistContent(t *testing.T) {
	o, _ := opts(t, "darwin")
	p := Plist(o)
	for _, want := range []string{
		"<key>Label</key><string>dev.mailai.daemon</string>",
		// mailai daemon run, XML-escaped
		"<array><string>/opt/mail &amp; co/mailai</string><string>daemon</string><string>run</string></array>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<key>MAILAI_HOME</key><string>/tmp/mh</string>",
		"<key>MAILAI_SOCKET</key><string>/tmp/mh/s.sock</string>",
		// The daemon logs to stderr; launchd writes it to the data dir's daemon.log.
		"<key>StandardOutPath</key><string>" + o.LogPath + "</string>",
		"<key>StandardErrorPath</key><string>" + o.LogPath + "</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist missing %q:\n%s", want, p)
		}
	}
}

func TestPlistWithoutEnvHasNoEnvironment(t *testing.T) {
	o, _ := opts(t, "darwin")
	o.Env = nil
	if p := Plist(o); strings.Contains(p, "EnvironmentVariables") {
		t.Fatalf("empty environment rendered:\n%s", p)
	}
}

func TestPlistPassesPlutil(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available (not macOS)")
	}
	o, _ := opts(t, "darwin")
	p := filepath.Join(t.TempDir(), "x.plist")
	if err := os.WriteFile(p, []byte(Plist(o)), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(plutil, "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v\n%s", err, out)
	}
}

func TestUnitContent(t *testing.T) {
	o, _ := opts(t, "linux")
	u := Unit(o)
	for _, want := range []string{
		"[Unit]\nDescription=mailai daemon\n",
		"ExecStart=\"/opt/mail & co/mailai\" daemon run\n",
		"Environment=\"MAILAI_HOME=/tmp/mh\"\n",
		"Environment=\"MAILAI_SOCKET=/tmp/mh/s.sock\"\n",
		"Restart=on-failure\n",
		"StandardOutput=append:" + o.LogPath + "\n",
		"StandardError=append:" + o.LogPath + "\n",
		"[Install]\nWantedBy=default.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

// systemd expands %-specifiers in unit files; a literal % must be doubled.
func TestUnitEscapesPercent(t *testing.T) {
	o, _ := opts(t, "linux")
	o.Bin = "/opt/100%/mailai"
	o.Env = map[string]string{"MAILAI_HOME": "/tmp/50%"}
	u := Unit(o)
	for _, want := range []string{`ExecStart="/opt/100%%/mailai" daemon run`, `Environment="MAILAI_HOME=/tmp/50%%"`} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestInstallLaunchd(t *testing.T) {
	o, r := opts(t, "darwin")
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(o.Home, "Library/LaunchAgents/dev.mailai.daemon.plist")
	if b, err := os.ReadFile(plist); err != nil || string(b) != Plist(o) {
		t.Fatalf("plist not written: %v", err)
	}
	want := [][]string{
		{"launchctl", "bootout", "gui/501", plist}, // a previous install is replaced
		{"launchctl", "bootstrap", "gui/501", plist},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	if _, err := os.Stat(filepath.Dir(o.LogPath)); err != nil {
		t.Fatal("log directory must exist before launchd opens the log")
	}
}

func TestInstallSystemd(t *testing.T) {
	o, r := opts(t, "linux")
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(o.Home, ".config/systemd/user/mailai.service")
	if b, err := os.ReadFile(unit); err != nil || string(b) != Unit(o) {
		t.Fatalf("unit not written: %v", err)
	}
	want := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "mailai.service"},
		{"systemctl", "--user", "restart", "mailai.service"}, // a running daemon picks up the new unit
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	if _, err := os.Stat(filepath.Dir(o.LogPath)); err != nil {
		t.Fatal("log directory must exist before systemd opens the log")
	}
}

func TestReinstallReplaces(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		o, r := opts(t, goos)
		if err := Install(o); err != nil {
			t.Fatal(err)
		}
		first := r.calls
		r.calls = nil
		o.Bin = "/usr/local/bin/mailai"
		if err := Install(o); err != nil {
			t.Fatalf("%s: reinstall: %v", goos, err)
		}
		if !reflect.DeepEqual(r.calls, first) {
			t.Errorf("%s: reinstall calls %q, first install %q", goos, r.calls, first)
		}
		b, _ := os.ReadFile(File(o))
		if !strings.Contains(string(b), "/usr/local/bin/mailai") || strings.Contains(string(b), "/opt/mail") {
			t.Errorf("%s: reinstall did not replace the definition:\n%s", goos, b)
		}
	}
}

func TestInstallLaunchdBootstrapFailureIsAnError(t *testing.T) {
	o, r := opts(t, "darwin")
	r.fail = map[string]bool{"launchctl bootstrap gui/501 " + File(o): true}
	if err := Install(o); err == nil {
		t.Fatal("bootstrap failure swallowed")
	}
}

func TestInstallSystemdFailureIsAnError(t *testing.T) {
	o, r := opts(t, "linux")
	r.fail = map[string]bool{"systemctl --user daemon-reload": true}
	if err := Install(o); err == nil {
		t.Fatal("systemctl failure swallowed")
	}
}

func TestUninstallLaunchd(t *testing.T) {
	o, r := opts(t, "darwin")
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	if err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"launchctl", "bootout", "gui/501", File(o)}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	if fileExists(File(o)) {
		t.Fatal("plist still there")
	}
}

func TestUninstallSystemd(t *testing.T) {
	o, r := opts(t, "linux")
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	if err := Uninstall(o); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"systemctl", "--user", "disable", "--now", "mailai.service"},
		{"systemctl", "--user", "daemon-reload"},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	if fileExists(File(o)) {
		t.Fatal("unit still there")
	}
}

// Uninstalling what was never installed is fine, even with no service
// manager answering (a box without a systemd user session).
func TestUninstallTwiceIsFine(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		o, r := opts(t, goos)
		r.fail = map[string]bool{
			"launchctl bootout gui/501 " + File(o):          true,
			"systemctl --user disable --now mailai.service": true,
			"systemctl --user daemon-reload":                true,
		}
		if err := Uninstall(o); err != nil {
			t.Errorf("%s: %v", goos, err)
		}
	}
}

func TestStatusNotInstalled(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		o, r := opts(t, goos)
		r.fail = map[string]bool{
			"launchctl print gui/501/dev.mailai.daemon":  true,
			"systemctl --user is-enabled mailai.service": true,
			"systemctl --user is-active mailai.service":  true,
		}
		s, err := Status(o)
		if err != nil {
			t.Fatal(err)
		}
		if s.Installed || s.Loaded || s.Running || s.Path != File(o) || s.Log != o.LogPath {
			t.Errorf("%s: %+v", goos, s)
		}
	}
}

func TestStatusLaunchd(t *testing.T) {
	o, r := opts(t, "darwin")
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	r.out = map[string]string{"launchctl print gui/501/dev.mailai.daemon": "gui/501/dev.mailai.daemon = {\n\tactive count = 1\n\tstate = running\n\tpid = 4242\n}\n"}
	s, err := Status(o)
	if err != nil {
		t.Fatal(err)
	}
	want := StatusInfo{Manager: "launchd", Path: File(o), Log: o.LogPath, Installed: true, Loaded: true, Running: true}
	if s != want {
		t.Fatalf("status %+v, want %+v", s, want)
	}
	if want := [][]string{{"launchctl", "print", "gui/501/dev.mailai.daemon"}}; !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	// Loaded but between restarts (KeepAlive throttling).
	r.out["launchctl print gui/501/dev.mailai.daemon"] = "gui/501/dev.mailai.daemon = {\n\tstate = not running\n}\n"
	if s, _ := Status(o); !s.Loaded || s.Running {
		t.Fatalf("status %+v", s)
	}
}

func TestStatusSystemd(t *testing.T) {
	o, r := opts(t, "linux")
	if err := Install(o); err != nil {
		t.Fatal(err)
	}
	r.calls = nil
	s, err := Status(o)
	if err != nil {
		t.Fatal(err)
	}
	want := StatusInfo{Manager: "systemd", Path: File(o), Log: o.LogPath, Installed: true, Loaded: true, Running: true}
	if s != want {
		t.Fatalf("status %+v, want %+v", s, want)
	}
	if want := [][]string{
		{"systemctl", "--user", "is-enabled", "mailai.service"},
		{"systemctl", "--user", "is-active", "mailai.service"},
	}; !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %q", r.calls)
	}
	r.fail = map[string]bool{"systemctl --user is-active mailai.service": true}
	if s, _ := Status(o); !s.Loaded || s.Running {
		t.Fatalf("status %+v", s)
	}
}

func TestUnsupportedOS(t *testing.T) {
	o, r := opts(t, "windows")
	if err := Install(o); err == nil || !strings.Contains(err.Error(), "windows") {
		t.Fatalf("install err = %v", err)
	}
	if err := Uninstall(o); err == nil || !strings.Contains(err.Error(), "windows") {
		t.Fatalf("uninstall err = %v", err)
	}
	if _, err := Status(o); err == nil || !strings.Contains(err.Error(), "windows") {
		t.Fatalf("status err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("calls = %q", r.calls)
	}
}

// Defaults carries the variables that place the daemon's files and socket
// into the service, and only when set.
// launchd and systemd start the daemon with a minimal PATH; the adapter
// runs `herdr` from the installer's PATH (/opt/homebrew/bin), so the unit
// carries it.
func TestDefaultsCarryPATH(t *testing.T) {
	t.Setenv("PATH", "/opt/homebrew/bin:/usr/bin:/bin")
	o, err := Defaults("/x/daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	if o.Env["PATH"] != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("env %v", o.Env)
	}
}

func TestDefaultsEnv(t *testing.T) {
	t.Setenv("PATH", "") // carried when set; TestDefaultsCarryPATH covers it
	t.Setenv("MAILAI_HOME", "/tmp/mh")
	t.Setenv("MAILAI_SOCKET", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	o, err := Defaults("/tmp/mh/data/daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"MAILAI_HOME": "/tmp/mh"}; !reflect.DeepEqual(o.Env, want) {
		t.Fatalf("env %v, want %v", o.Env, want)
	}
	if !filepath.IsAbs(o.Bin) || o.Runner == nil || o.LogPath != "/tmp/mh/data/daemon.log" || o.Home == "" {
		t.Fatalf("defaults %+v", o)
	}
	t.Setenv("MAILAI_SOCKET", "/tmp/s.sock")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data") // config.DefaultPaths reads it too
	o, _ = Defaults("/x/daemon.log")
	if o.Env["MAILAI_SOCKET"] != "/tmp/s.sock" || o.Env["XDG_DATA_HOME"] != "/tmp/xdg-data" {
		t.Fatalf("env %v", o.Env)
	}
}
