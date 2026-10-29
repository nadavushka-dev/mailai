//go:build !linux && !darwin

package procinfo

import "net"

// PeerPID is 0 where the kernel does not give the peer's pid; identity then
// rests on tokens alone (SPEC §9: not supported in v1).
func PeerPID(net.Conn) int { return 0 }

// ParentPID cannot be read here.
func ParentPID(int) (int, bool) { return 0, false }
