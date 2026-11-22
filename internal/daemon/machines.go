package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/nadavushka-dev/mailai/internal/proto"
)

// Machine keys (SPEC §13a): every daemon keeps a random key in its data
// dir and presents it in link-hello. The inbox machine pins sha256(key) per
// machine name on first use and refuses the name with any other key until
// the person runs `mailai machine forget <name>`.

const MachineKeySize = 32

// LoadMachineKey reads the key at path, creating it (0600) the first time.
func LoadMachineKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		k := make([]byte, MachineKeySize)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return LoadMachineKey(path)
		}
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(k); err != nil {
			f.Close()
			os.Remove(path)
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) != MachineKeySize {
		return nil, fmt.Errorf("%s: a machine key is %d bytes, this one is %d; remove it to make a new one (the inbox machine then needs `mailai machine forget`)", path, MachineKeySize, len(b))
	}
	return b, nil
}

func newMachineKey() []byte {
	k := make([]byte, MachineKeySize)
	rand.Read(k)
	return k
}

func keyPin(k []byte) string {
	h := sha256.Sum256(k)
	return hex.EncodeToString(h[:])
}

// machinesFile is machines.json on the inbox machine. The first format was
// a bare JSON list of names; loadKnown still reads it.
type machinesFile struct {
	Machines []machineRecord `json:"machines"`
}

type machineRecord struct {
	Name string `json:"name"`
	// KeySHA256 is the pinned key's hash; empty until the next hello pins it.
	KeySHA256 string `json:"key_sha256,omitempty"`
}

// MachineInfo is one row of `mailai machine ls`.
type MachineInfo struct {
	Name   string `json:"name"`
	Linked bool   `json:"linked"`
	Pinned bool   `json:"pinned"`
}

type MachineListResult struct {
	Machines []MachineInfo `json:"machines"`
}

type MachineForgetArgs struct {
	Name string `json:"name"`
}

// checkPin admits a hello for machine with key, pinning an unpinned name
// when a key is presented. It reports whether machines.json must be saved.
// Called with d.mu held.
func (d *Daemon) checkPin(machine string, key []byte) (bool, error) {
	pin, known := d.known[machine]
	if pin != "" {
		if len(key) == 0 || subtle.ConstantTimeCompare([]byte(keyPin(key)), []byte(pin)) != 1 {
			return false, errForbidden("machine %s presented a different machine key than the one pinned here; if %s was reinstalled, run `mailai machine forget %s` on this machine", machine, machine, machine)
		}
		return false, nil
	}
	if len(key) == 0 {
		// An older spoke with no key: known, left unpinned.
		d.known[machine] = ""
		return !known, nil
	}
	d.known[machine] = keyPin(key)
	return true, nil
}

func (d *Daemon) machineVerb(c *conn, verb string, raw []byte) (any, error) {
	if !isPerson(c.pl) {
		return nil, proto.Errorf(proto.CodeNotHuman, "only the person, from outside every pane, manages machines")
	}
	switch verb {
	case "machine-list":
		d.mu.Lock()
		out := []MachineInfo{}
		for name, pin := range d.known {
			out = append(out, MachineInfo{Name: name, Linked: d.links[name] != nil, Pinned: pin != ""})
		}
		d.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return MachineListResult{Machines: out}, nil
	case "machine-forget":
		a, err := decode[MachineForgetArgs](raw)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		_, known := d.known[a.Name]
		live := d.links[a.Name]
		delete(d.known, a.Name)
		d.mu.Unlock()
		if !known {
			return nil, proto.Errorf(proto.CodeNotFound, "no machine %q has linked here", a.Name)
		}
		if live != nil {
			live.Close()
		}
		d.saveKnown()
		d.log.Printf("machine forgotten: %s", a.Name)
		return map[string]string{"forgot": a.Name}, nil
	}
	return nil, proto.Errorf(proto.CodeUnknownVerb, "%q", verb)
}

func (d *Daemon) loadKnown() {
	if d.knownFile == "" {
		return
	}
	b, err := os.ReadFile(d.knownFile)
	if err != nil {
		return
	}
	var names []string
	if json.Unmarshal(b, &names) == nil {
		for _, n := range names {
			d.known[n] = ""
		}
		return
	}
	var f machinesFile
	if err := json.Unmarshal(b, &f); err != nil {
		d.log.Printf("%s: %v", d.knownFile, err)
		return
	}
	for _, m := range f.Machines {
		d.known[m.Name] = m.KeySHA256
	}
}

func (d *Daemon) saveKnown() {
	if d.knownFile == "" {
		return
	}
	d.knownMu.Lock()
	defer d.knownMu.Unlock()
	d.mu.Lock()
	f := machinesFile{Machines: []machineRecord{}}
	for n, pin := range d.known {
		f.Machines = append(f.Machines, machineRecord{Name: n, KeySHA256: pin})
	}
	d.mu.Unlock()
	sort.Slice(f.Machines, func(i, j int) bool { return f.Machines[i].Name < f.Machines[j].Name })
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp := d.knownFile + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, d.knownFile)
	}
}
