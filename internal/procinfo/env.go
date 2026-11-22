package procinfo

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// EnvironVar finds name in a NUL-separated environment block, the layout of
// /proc/<pid>/environ and of the tail of darwin's kern.procargs2.
func EnvironVar(block []byte, name string) (string, bool) {
	prefix := []byte(name + "=")
	for _, entry := range bytes.Split(block, []byte{0}) {
		if v, ok := bytes.CutPrefix(entry, prefix); ok {
			return string(v), true
		}
	}
	return "", false
}

// CmdlineArgs splits /proc/<pid>/cmdline into argv. A process that rewrote
// its title leaves NUL padding, so trailing empty arguments are dropped.
func CmdlineArgs(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// ProcargsArgs reads argv out of a kern.procargs2 buffer (layout below).
func ProcargsArgs(buf []byte) ([]string, bool) {
	if len(buf) < 4 {
		return nil, false
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	rest := buf[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 || argc < 0 || argc > 4096 {
		return nil, false
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	args := make([]string, 0, argc)
	for i := 0; i < argc; i++ {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, false
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return args, true
}

// ProcargsEnvVar reads one environment variable out of a kern.procargs2
// buffer: int32 argc, the executable path, NUL padding, argc arguments, then
// the environment, each NUL-terminated.
func ProcargsEnvVar(buf []byte, name string) (string, bool) {
	if len(buf) < 4 {
		return "", false
	}
	argc := int(int32(binary.LittleEndian.Uint32(buf[:4])))
	rest := buf[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 || argc < 0 {
		return "", false
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	for i := 0; i < argc; i++ {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return "", false
		}
		rest = rest[end+1:]
	}
	if stop := bytes.Index(rest, []byte{0, 0}); stop >= 0 {
		rest = rest[:stop]
	}
	return EnvironVar(rest, name)
}
