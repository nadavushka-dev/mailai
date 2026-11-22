package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nadavushka-dev/mailai/internal/client"
	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// Machine keys (SPEC §13a): a spoke proves it is the machine that first
// linked under its name with a random key it keeps; the inbox machine pins
// the key's hash per name, trust on first use.

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func pinOf(k []byte) string {
	h := sha256.Sum256(k)
	return hex.EncodeToString(h[:])
}

func hubWithFile(t *testing.T, file string) string {
	t.Helper()
	sock, _ := startDaemonWith(t, Options{Name: "s1", Store: store.New(store.Options{MachineID: "s1"}), MachinesFile: file,
		Placer: &fakePlacer{byToken: map[string]Placement{
			"detached": {Proof: store.ProofDetached},
			"t-worker": {Proof: store.ProofToken, Principal: "worker"},
		}}})
	return sock
}

func machineList(t *testing.T, sock string) []MachineInfo {
	t.Helper()
	var r MachineListResult
	if err := dial(t, sock).Call("machine-list", nil, &r); err != nil {
		t.Fatal(err)
	}
	return r.Machines
}

func readMachinesFile(t *testing.T, file string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var f machinesFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	out := map[string]string{}
	for _, m := range f.Machines {
		out[m.Name] = m.KeySHA256
	}
	return out
}

func TestMachineKey_CreatedOnceAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine.key")
	k1, err := LoadMachineKey(path)
	if err != nil || len(k1) != 32 {
		t.Fatalf("%x %v", k1, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi.Mode(), err)
	}
	k2, err := LoadMachineKey(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("key changed on reload: %x %x %v", k1, k2, err)
	}
	other, _ := LoadMachineKey(filepath.Join(t.TempDir(), "machine.key"))
	if bytes.Equal(k1, other) {
		t.Fatal("two machines got the same key")
	}
	os.WriteFile(path, []byte("short"), 0o600)
	if _, err := LoadMachineKey(path); err == nil {
		t.Fatal("a damaged key file was accepted")
	}
}

func TestStar_MachineKeyPinnedOnFirstHello(t *testing.T) {
	file := filepath.Join(t.TempDir(), "machines.json")
	sock := hubWithFile(t, file)
	if err := dial(t, sock).Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readMachinesFile(t, file); got["s2"] != pinOf(key(1)) {
		t.Fatalf("not pinned: %v", got)
	}
	ms := machineList(t, sock)
	if len(ms) != 1 || ms[0] != (MachineInfo{Name: "s2", Linked: true, Pinned: true}) {
		t.Fatalf("%+v", ms)
	}
}

func TestStar_MachineKeyMismatchRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "machines.json")
	sock := hubWithFile(t, file)
	first := dial(t, sock)
	if err := first.Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	for _, k := range [][]byte{key(2), nil} {
		err := dial(t, sock).Call("link-hello", LinkHelloArgs{Machine: "s2", Key: k}, nil)
		if client.Code(err) != proto.CodeForbidden || !strings.Contains(err.Error(), "mailai machine forget s2") {
			t.Fatalf("key %x: %v", k, err)
		}
	}
	if err := dial(t, sock).Call("link-hello", LinkHelloArgs{Machine: "s3", Key: []byte("short")}, nil); client.Code(err) != proto.CodeBadRequest {
		t.Fatalf("short key: %v", err)
	}
	// The pinned machine's link is untouched.
	if err := first.Call("link-flush", nil, nil); err != nil {
		t.Fatalf("pinned link dropped: %v", err)
	}
	if got := readMachinesFile(t, file); got["s2"] != pinOf(key(1)) {
		t.Fatalf("pin moved: %v", got)
	}
}

func TestStar_ForgetAllowsRepin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "machines.json")
	sock := hubWithFile(t, file)
	first := dial(t, sock)
	if err := first.Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	person := dial(t, sock)
	if err := person.Call("machine-forget", MachineForgetArgs{Name: "s9"}, nil); client.Code(err) != proto.CodeNotFound {
		t.Fatalf("forget unknown: %v", err)
	}
	if err := person.Call("machine-forget", MachineForgetArgs{Name: "s2"}, nil); err != nil {
		t.Fatal(err)
	}
	if ms := machineList(t, sock); len(ms) != 0 {
		t.Fatalf("forgotten machine listed: %+v", ms)
	}
	// Its live link is closed with it.
	if err := first.Call("link-flush", nil, nil); err == nil {
		t.Fatal("the forgotten machine's link is still up")
	}
	if err := dial(t, sock).Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(2)}, nil); err != nil {
		t.Fatalf("re-pin after forget: %v", err)
	}
	if got := readMachinesFile(t, file); got["s2"] != pinOf(key(2)) {
		t.Fatalf("not re-pinned: %v", got)
	}
}

// machines.json used to be a list of names. Those stay known, unpinned
// until their next hello pins them.
func TestStar_MachinesFileMigrates(t *testing.T) {
	file := filepath.Join(t.TempDir(), "machines.json")
	os.WriteFile(file, []byte(`["s2","s3"]`), 0o600)
	sock := hubWithFile(t, file)
	ms := machineList(t, sock)
	want := []MachineInfo{{Name: "s2"}, {Name: "s3"}}
	if len(ms) != 2 || ms[0] != want[0] || ms[1] != want[1] {
		t.Fatalf("%+v", ms)
	}
	if err := dial(t, sock).Call("send", SendArgs{From: "reviewer", To: "worker@s3", Text: "still known"}, nil); err != nil {
		t.Fatal(err)
	}
	// A keyless hello (an older spoke) leaves an unpinned name unpinned.
	if err := dial(t, sock).Call("link-hello", LinkHelloArgs{Machine: "s3"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := dial(t, sock).Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	got := readMachinesFile(t, file)
	if len(got) != 2 || got["s2"] != pinOf(key(1)) || got["s3"] != "" {
		t.Fatalf("%v", got)
	}
	// A new daemon on the same file keeps the pin.
	sock2 := hubWithFile(t, file)
	if err := dial(t, sock2).Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(2)}, nil); client.Code(err) != proto.CodeForbidden {
		t.Fatalf("pin lost across restart: %v", err)
	}
}

// Only the person lists or forgets machines: not a link, not a detached
// caller, not an agent.
func TestMachineVerbsArePersonOnly(t *testing.T) {
	sock := hubWithFile(t, "")
	l := dial(t, sock)
	if err := l.Call("link-hello", LinkHelloArgs{Machine: "s2", Key: key(1)}, nil); err != nil {
		t.Fatal(err)
	}
	det, err := client.Dial(sock, "detached")
	if err != nil {
		t.Fatal(err)
	}
	defer det.Close()
	agent := dialAs(t, sock, "worker")
	for _, c := range []*client.Client{l, det, agent} {
		if err := c.Call("machine-list", nil, nil); err == nil {
			t.Fatal("machine-list from a non-person")
		}
		if err := c.Call("machine-forget", MachineForgetArgs{Name: "s2"}, nil); err == nil {
			t.Fatal("machine-forget from a non-person")
		}
	}
	if ms := machineList(t, sock); len(ms) != 1 || !ms[0].Linked {
		t.Fatalf("%+v", ms)
	}
}

// The spoke presents its key, so the inbox machine pins it.
func TestStar_SpokePresentsItsKey(t *testing.T) {
	s := newStar(t)
	ms := machineList(t, s.hubSock)
	if len(ms) != 1 || ms[0] != (MachineInfo{Name: "s2", Linked: true, Pinned: true}) {
		t.Fatalf("%+v", ms)
	}
}
