package tui

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// systemOpen shows a file with the desktop's viewer: open on macOS,
// xdg-open elsewhere. Over ssh there is no desktop; the caller then says
// where the file is.
func systemOpen(path string) error {
	name := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
	}
	if _, err := exec.LookPath(name); err != nil {
		return errors.New(name + " not found")
	}
	return exec.Command(name, path).Run()
}
