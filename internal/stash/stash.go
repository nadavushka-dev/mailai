// Package stash keeps files for a session (SPEC §6): a sender-owned
// attachment may be gone by the time it is read, a stashed one lives until
// the session ends. The daemon owns the stash directory.
package stash

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Stash is one machine's stash root.
type Stash struct{ root string }

// New makes a Stash under root (created on first use).
func New(root string) *Stash { return &Stash{root: root} }

// Entry describes a stashed file.
type Entry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

var errBadSession = errors.New("session name may not contain path separators")

func (s *Stash) dir(session string) (string, error) {
	if session == "" || strings.ContainsAny(session, `/\`) || session == "." || session == ".." {
		return "", errBadSession
	}
	return filepath.Join(s.root, session), nil
}

// Put copies an absolute, regular file into the session's stash and returns
// the stored path. The name carries a content hash prefix, so two puts of a
// changed file never collide and the same bytes land once.
func (s *Stash) Put(session, src string) (string, error) {
	dir, err := s.dir(session)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(src) {
		return "", fmt.Errorf("stash put: %s is not an absolute path", src)
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("stash put: %s is not a regular file", src)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	dest := filepath.Join(dir, hex.EncodeToString(h.Sum(nil))[:12]+"-"+filepath.Base(src))
	if err := os.Rename(tmp.Name(), dest); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	os.Chmod(dest, st.Mode().Perm()&0o700|0o400)
	return dest, nil
}

// Owns says whether a path is inside this stash.
func (s *Stash) Owns(path string) bool {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && filepath.IsAbs(path)
}

// Get copies a stashed file to dest. Only stashed paths are served.
func (s *Stash) Get(stored, dest string) error {
	if !s.Owns(stored) {
		return fmt.Errorf("stash get: %s is not in the stash", stored)
	}
	in, err := os.Open(stored)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// List returns the session's stashed files, by name.
func (s *Stash) List(session string) ([]Entry, error) {
	dir, err := s.dir(session)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		if !de.Type().IsRegular() || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		name := de.Name()
		if i := strings.IndexByte(name, '-'); i == 12 {
			name = name[13:]
		}
		out = append(out, Entry{Path: filepath.Join(dir, de.Name()), Name: name, Bytes: info.Size()})
	}
	return out, nil
}

// EndSession removes everything stashed for a session.
func (s *Stash) EndSession(session string) error {
	dir, err := s.dir(session)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
