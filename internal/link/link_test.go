package link

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHArgsRunBridgeOnItsOwnConnection(t *testing.T) {
	got := strings.Join(SSHArgs("s1", "", "bridge"), " ")
	for _, want := range []string{"-T", "BatchMode=yes", "ControlMaster=no", "ControlPath=none", "s1 mailai bridge"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing %q", got, want)
		}
	}
}

// The bridge copies both ways between stdio and the daemon socket.
func TestBridgeRelaysToTheSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(c)
		c.Write(append([]byte("echo:"), b...))
		c.Close()
	}()
	var out bytes.Buffer
	if err := Bridge(strings.NewReader("hello"), &out, sock); err != nil {
		t.Fatal(err)
	}
	if out.String() != "echo:hello" {
		t.Fatalf("%q", out.String())
	}
	if err := Bridge(strings.NewReader(""), io.Discard, filepath.Join(t.TempDir(), "none")); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("no daemon: %v", err)
	}
}

// Dial reports what ssh said when the session ends.
func TestDialReportsSSHStderr(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "ssh")
	os.WriteFile(fake, []byte("#!/bin/sh\necho 'Permission denied (publickey).' >&2\nexit 255\n"), 0o755)
	t.Setenv("MAILAI_SSH", fake)
	c, err := Dial(context.Background(), "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = io.ReadAll(c)
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("%v", err)
	}
}
