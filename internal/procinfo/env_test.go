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

// argv, for telling an agent program from a shell (SPEC §9).
func TestProcargsArgsAndCmdline(t *testing.T) {
	var buf []byte
	buf = binary.LittleEndian.AppendUint32(buf, 3)
	buf = append(buf, "/usr/local/bin/node\x00\x00\x00"...)
	buf = append(buf, "node\x00/x/claude-code/cli.js\x00--resume\x00"...)
	buf = append(buf, "HOME=/h\x00\x00"...)
	if a, ok := ProcargsArgs(buf); !ok || len(a) != 3 || a[1] != "/x/claude-code/cli.js" {
		t.Fatalf("%q %v", a, ok)
	}
	if _, ok := ProcargsArgs([]byte{1}); ok {
		t.Fatal("short buffer")
	}
	// A process that rewrote its title pads argv with NULs (pi does).
	if a := CmdlineArgs([]byte("pi\x00\x00\x00\x00")); len(a) != 1 || a[0] != "pi" {
		t.Fatalf("%q", a)
	}
	if a := CmdlineArgs([]byte("bash\x00-c\x00mailai unread\x00")); len(a) != 3 || a[2] != "mailai unread" {
		t.Fatalf("%q", a)
	}
}

// The kernel reports this process's own argv.
func TestArgsOnSelf(t *testing.T) {
	a, ok := Args(os.Getpid())
	if !ok || len(a) == 0 || a[0] != os.Args[0] {
		t.Fatalf("%q %v, want argv[0] %q", a, ok, os.Args[0])
	}
}

// The kernel reports this process's own environment.
func TestEnvVarOnSelf(t *testing.T) {
	os.Setenv("MAILAI_PROCINFO_TEST", "yes") // not visible: the kernel keeps the start-time env
	if v, ok := EnvVar(os.Getpid(), "PATH"); !ok || v == "" {
		t.Skipf("kernel gave no environment for self: %v %q", ok, v)
	}
}
