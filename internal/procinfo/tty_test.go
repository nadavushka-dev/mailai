package procinfo

import (
	"os"
	"testing"
)

// HasTTY agrees with the kernel's own answer for this process: /dev/tty
// opens only for a process with a controlling terminal.
func TestHasTTYMatchesDevTTY(t *testing.T) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	want := err == nil
	if f != nil {
		f.Close()
	}
	if got := HasTTY(os.Getpid()); got != want {
		t.Fatalf("HasTTY(self) = %v, /dev/tty opens = %v", got, want)
	}
	if HasTTY(-1) || HasTTY(0) {
		t.Fatal("no pid has no terminal")
	}
}
