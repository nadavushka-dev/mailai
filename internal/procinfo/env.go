package procinfo

import (
	"bytes"
	"encoding/binary"
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
