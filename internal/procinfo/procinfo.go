// Package procinfo reads what the kernel says about a process: the pid on
// the other end of a unix socket and a process's parent. Nothing a caller
// sends can change either, which is what identity (SPEC §9) rests on.
//
// The platform files are adapted from TUIOS (github.com/Gaurav-Gosain/tuios,
// MIT), internal/procinfo.
package procinfo

import "net"

// Reader is the kernel view the identity package depends on; a fake stands
// in for it in tests.
type Reader interface {
	PeerPID(conn net.Conn) int
	ParentPID(pid int) (int, bool)
}

// Kernel is the real Reader.
type Kernel struct{}

func (Kernel) PeerPID(conn net.Conn) int     { return PeerPID(conn) }
func (Kernel) ParentPID(pid int) (int, bool) { return ParentPID(pid) }
