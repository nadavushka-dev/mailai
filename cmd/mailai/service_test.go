package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/service"
)

// The service commands are tested in process with a fake service manager:
// a test must never install or start a real launchd agent or systemd unit.

type fakeManager struct{ calls [][]string }

func (f *fakeManager) Run(name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "launchctl" && args[0] == "print" {
		return "state = running\n", nil
	}
	return "", nil
}

func serviceOpts(t *testing.T, goos string) (service.Options, *fakeManager) {
	home := t.TempDir()
	f := &fakeManager{}
	return service.Options{GOOS: goos, Home: home, UID: 501, Bin: "/opt/mailai",
		LogPath: filepath.Join(home, "data", "daemon.log"), Runner: f}, f
}

func TestCLI_ServiceInstallStatusUninstall(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		o, f := serviceOpts(t, goos)
		var out bytes.Buffer
		if err := runService("install", o, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), service.File(o)) || !strings.Contains(out.String(), o.LogPath) {
			t.Errorf("%s install: %q", goos, out.String())
		}
		if _, err := os.Stat(service.File(o)); err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if len(f.calls) == 0 {
			t.Fatalf("%s: service manager not told", goos)
		}
		out.Reset()
		if err := runService("status", o, &out); err != nil {
			t.Fatal(err)
		}
		if s := out.String(); !strings.Contains(s, "installed") || !strings.Contains(s, "running") || !strings.Contains(s, service.File(o)) {
			t.Errorf("%s status: %q", goos, s)
		}
		out.Reset()
		if err := runService("uninstall", o, &out); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(service.File(o)); err == nil {
			t.Fatalf("%s: unit still there", goos)
		}
		if !strings.Contains(out.String(), "removed") {
			t.Errorf("%s uninstall: %q", goos, out.String())
		}
	}
}

func TestCLI_ServiceStatusJSON(t *testing.T) {
	o, _ := serviceOpts(t, "darwin")
	jsonOut = true
	defer func() { jsonOut = false }()
	var out bytes.Buffer
	if err := runService("install", o, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runService("status", o, &out); err != nil {
		t.Fatal(err)
	}
	var got service.StatusInfo
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v: %q", err, out.String())
	}
	want := service.StatusInfo{Manager: "launchd", Path: service.File(o), Log: o.LogPath, Installed: true, Loaded: true, Running: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("status %+v, want %+v", got, want)
	}
}

func TestCLI_ServiceUnknownSubcommand(t *testing.T) {
	o, f := serviceOpts(t, "darwin")
	if err := runService("start", o, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "install|uninstall|status") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %q", f.calls)
	}
	// As a process too: a bad subcommand fails before any service manager runs.
	out, err := exec.Command(bin, "service", "start").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "install|uninstall|status") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestCLI_UsageListsService(t *testing.T) {
	out, err := exec.Command(bin, "help").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "service install|uninstall|status") {
		t.Fatalf("%v %s", err, out)
	}
}
