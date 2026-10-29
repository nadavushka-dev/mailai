package procinfo

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The kernel names this very process on both ends of a socketpair, and its
// parent is the go test runner, which is a live pid.
func TestPeerPIDAndParentPIDOnSelf(t *testing.T) {
	sock := filepath.Join(os.TempDir(), "mailai-procinfo-test.sock")
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	defer os.Remove(sock)
	done := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- -1
			return
		}
		defer c.Close()
		done <- PeerPID(c)
	}()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := <-done; got != os.Getpid() {
		t.Fatalf("peer pid %d, want %d", got, os.Getpid())
	}
	ppid, ok := ParentPID(os.Getpid())
	if !ok || ppid != os.Getppid() {
		t.Fatalf("parent %d %v, want %d", ppid, ok, os.Getppid())
	}
	if _, ok := ParentPID(0); ok {
		t.Fatal("pid 0 has no parent")
	}
	if PeerPID(nil) != 0 {
		t.Fatal("nil conn")
	}
}
