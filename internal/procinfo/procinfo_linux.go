//go:build linux

package procinfo

import (
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// PeerPID returns the pid of the process on the other end of a unix socket,
// as the kernel recorded it at connect time (SO_PEERCRED), or 0 when it
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
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil && cred != nil {
			pid = int(cred.Pid)
		}
	})
	return pid
}

// EnvVar reads one variable of the environment process pid was started with.
// /proc/<pid>/environ is readable only for a process of the same user.
func EnvVar(pid int, name string) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return "", false
	}
	return EnvironVar(data, name)
}

// statFields are the fields of /proc/<pid>/stat after comm: state(3)
// ppid(4) pgrp(5) session(6) tty_nr(7) ... comm (field 2) is in
// parentheses and may hold spaces, so count from after the last ')'.
func statFields(pid int) []string {
	if pid <= 0 {
		return nil
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil
	}
	stat := string(data)
	rparen := strings.LastIndexByte(stat, ')')
	if rparen < 0 {
		return nil
	}
	return strings.Fields(stat[rparen+1:])
}

// ParentPID returns a process's parent pid, field 4 of /proc/<pid>/stat.
func ParentPID(pid int) (int, bool) {
	fields := statFields(pid)
	if len(fields) < 2 {
		return 0, false
	}
	v, err := strconv.Atoi(fields[1])
	return v, err == nil
}

// HasTTY says whether pid has a controlling terminal (tty_nr, field 7).
func HasTTY(pid int) bool {
	fields := statFields(pid)
	if len(fields) < 5 {
		return false
	}
	v, err := strconv.Atoi(fields[4])
	return err == nil && v != 0
}
