package daemon

import (
	"net"

	"github.com/nadavushka-dev/mailai/internal/procinfo"
)

type realProc struct{}

func (realProc) PeerPID(c net.Conn) int                { return procinfo.PeerPID(c) }
func (realProc) ParentPID(pid int) (int, bool)         { return procinfo.ParentPID(pid) }
func (realProc) EnvVar(p int, n string) (string, bool) { return procinfo.EnvVar(p, n) }
