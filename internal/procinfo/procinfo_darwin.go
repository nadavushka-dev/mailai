//go:build darwin

package procinfo

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerPID returns the pid of the process on the other end of a unix socket,
// as the kernel recorded it at connect time (LOCAL_PEERPID), or 0 when it
// does not say.
func PeerPID(conn net.Conn) int {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if v, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
			pid = v
		}
	})
	return pid
}

// EnvVar reads one variable of the environment process pid was started with,
// from kern.procargs2, which the kernel gives only for a process of the same
// user. A refusal reads as absent.
func EnvVar(pid int, name string) (string, bool) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", false
	}
	return ProcargsEnvVar(buf, name)
}

// Args reads a process's argv from kern.procargs2 (same user only).
func Args(pid int) ([]string, bool) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, false
	}
	return ProcargsArgs(buf)
}

// HasTTY says whether pid has a controlling terminal (e_tdev != NODEV).
func HasTTY(pid int) bool {
	if pid <= 0 {
		return false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return false
	}
	return kp.Eproc.Tdev != -1 && kp.Eproc.Tdev != 0
}

// ParentPID returns a process's parent pid from its kinfo_proc.
func ParentPID(pid int) (int, bool) {
	if pid <= 0 {
		return 0, false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	return int(kp.Eproc.Ppid), true
}
