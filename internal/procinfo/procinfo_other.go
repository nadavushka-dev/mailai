//go:build !linux && !darwin

package procinfo

import "net"

// PeerPID is 0 where the kernel does not give the peer's pid; identity then
// rests on tokens alone (SPEC §9: not supported in v1).
func PeerPID(net.Conn) int { return 0 }

// EnvVar cannot be read here.
func EnvVar(int, string) (string, bool) { return "", false }

// HasTTY cannot be read here: no caller is placed at a terminal.
func HasTTY(int) bool { return false }

// Args cannot be read here.
func Args(int) ([]string, bool) { return nil, false }

// ParentPID cannot be read here.
func ParentPID(int) (int, bool) { return 0, false }
