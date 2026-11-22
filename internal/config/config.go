// Package config is the daemon's paths and config.toml (SPEC §11).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
func (p Paths) Stash() string      { return filepath.Join(p.Data, "stash") }
func (p Paths) Tokens() string     { return filepath.Join(p.Data, "tokens.json") }
func (p Paths) Identity() string   { return filepath.Join(p.Data, "identity.toml") }
func (p Paths) ConfigFile() string { return filepath.Join(p.Config, "config.toml") }
func (p Paths) Log() string        { return filepath.Join(p.Data, "daemon.log") }
func (p Paths) Machines() string   { return filepath.Join(p.Data, "machines.json") }
func (p Paths) MachineKey() string { return filepath.Join(p.Data, "machine.key") }

// Config is config.toml.
type Config struct {
	Adapter string `toml:"adapter"`
	Mail    struct {
		Wake *bool `toml:"wake"`
	} `toml:"mail"`
	Notify struct {
		Enabled *bool `toml:"enabled"`
	} `toml:"notify"`
	Identity struct {
		RequireTerminal *bool `toml:"require_terminal"`
	} `toml:"identity"`
	// Fleet places this machine in the star (SPEC §13a).
	Fleet struct {
		// Name is this machine's name; the short hostname when empty.
		Name string `toml:"name"`
		// Inbox is the inbox machine's ssh target; empty on the inbox machine.
		Inbox string `toml:"inbox"`
		// Command is mailai on the inbox machine ("mailai").
		Command string `toml:"command"`
	} `toml:"fleet"`
}

// MachineName is [fleet] name, else the short hostname.
func (c Config) MachineName() string {
	if c.Fleet.Name != "" {
		return c.Fleet.Name
	}
	h, _ := os.Hostname()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	if h == "" {
		h = "local"
	}
	return h
}

// Wake says whether resting agents are woken (default true).
func (c Config) Wake() bool { return c.Mail.Wake == nil || *c.Mail.Wake }

// RequireTerminal says only a caller at a terminal is the person (§9);
// on unless [identity] require_terminal = false (test rigs).
func (c Config) RequireTerminal() bool {
	return c.Identity.RequireTerminal == nil || *c.Identity.RequireTerminal
}

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
