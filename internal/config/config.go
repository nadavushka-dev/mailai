// Package config is the daemon's paths and config.toml (SPEC §11).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Paths are where mailai keeps things on this machine.
type Paths struct {
	Data   string // $XDG_DATA_HOME/mailai
	Config string // $XDG_CONFIG_HOME/mailai
}

// DefaultPaths resolves XDG with MAILAI_HOME overriding both for tests.
func DefaultPaths() (Paths, error) {
	if h := os.Getenv("MAILAI_HOME"); h != "" {
		return Paths{Data: filepath.Join(h, "data"), Config: filepath.Join(h, "config")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	return Paths{Data: filepath.Join(data, "mailai"), Config: filepath.Join(cfg, "mailai")}, nil
}

// Socket is the daemon's unix socket. MAILAI_SOCKET overrides it, which is
// also how a client reaches a daemon in a non-default place.
func (p Paths) Socket() string {
	if s := os.Getenv("MAILAI_SOCKET"); s != "" {
		return s
	}
	return filepath.Join(p.Data, "mailai.sock")
}

func (p Paths) StoreDB() string    { return filepath.Join(p.Data, "store.db") }
func (p Paths) Identity() string   { return filepath.Join(p.Data, "identity.toml") }
func (p Paths) ConfigFile() string { return filepath.Join(p.Config, "config.toml") }
func (p Paths) Log() string        { return filepath.Join(p.Data, "daemon.log") }

// Config is config.toml.
type Config struct {
	Adapter string `toml:"adapter"`
	Mail    struct {
		Wake *bool `toml:"wake"`
	} `toml:"mail"`
	Notify struct {
		Enabled *bool `toml:"enabled"`
	} `toml:"notify"`
}

// Wake says whether resting agents are woken (default true).
func (c Config) Wake() bool { return c.Mail.Wake == nil || *c.Mail.Wake }

// Load reads config.toml; a missing file is the default config.
func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}
