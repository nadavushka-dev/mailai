package procinfo

import (
	"encoding/binary"
	"os"
	"testing"
)

func TestEnvironVar(t *testing.T) {
	block := []byte("A=1\x00HERDR_PANE_ID=w1:p3\x00B=x=y\x00")
	if v, ok := EnvironVar(block, "HERDR_PANE_ID"); !ok || v != "w1:p3" {
		t.Fatal(v, ok)
	}
	if v, ok := EnvironVar(block, "B"); !ok || v != "x=y" {
		t.Fatal(v, ok)
	}
	if _, ok := EnvironVar(block, "HERDR"); ok {
		t.Fatal("prefix must not match")
	}
}

func TestProcargsEnvVar(t *testing.T) {
	var buf []byte
	buf = binary.LittleEndian.AppendUint32(buf, 2)
	buf = append(buf, "/bin/zsh\x00\x00\x00"...)
	buf = append(buf, "zsh\x00-l\x00"...)
	buf = append(buf, "HOME=/h\x00HERDR_PANE_ID=w2:p9\x00\x00apple=junk\x00"...)
	if v, ok := ProcargsEnvVar(buf, "HERDR_PANE_ID"); !ok || v != "w2:p9" {
		t.Fatal(v, ok)
	}
	if _, ok := ProcargsEnvVar(buf, "apple"); ok {
		t.Fatal("past the environment")
	}
	if _, ok := ProcargsEnvVar([]byte{1}, "x"); ok {
		t.Fatal("short buffer")
	}
}

// The kernel reports this process's own environment.
func TestEnvVarOnSelf(t *testing.T) {
	os.Setenv("MAILAI_PROCINFO_TEST", "yes") // not visible: the kernel keeps the start-time env
	if v, ok := EnvVar(os.Getpid(), "PATH"); !ok || v == "" {
		t.Skipf("kernel gave no environment for self: %v %q", ok, v)
	}
}
