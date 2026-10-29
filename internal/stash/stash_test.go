package stash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPutGetList(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	src := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(src, []byte("hello"), 0o644)
	stored, err := s.Put("api", src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, filepath.Join(root, "api")+string(filepath.Separator)) || !strings.HasSuffix(stored, "-notes.md") {
		t.Fatalf("stored at %s", stored)
	}
	if !s.Owns(stored) || s.Owns(src) {
		t.Fatal("Owns must recognise only stashed paths")
	}
	// Changing or removing the source does not touch the stashed copy.
	os.Remove(src)
	b, _ := os.ReadFile(stored)
	if string(b) != "hello" {
		t.Fatal(string(b))
	}
	dest := filepath.Join(t.TempDir(), "out.md")
	if err := s.Get(stored, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "hello" {
		t.Fatal(string(b))
	}
	list, err := s.List("api")
	if err != nil || len(list) != 1 || list[0].Path != stored || list[0].Bytes != 5 || list[0].Name != "notes.md" {
		t.Fatalf("%+v %v", list, err)
	}
	if l, _ := s.List("other"); len(l) != 0 {
		t.Fatal("empty session must list nothing")
	}
}

func TestPutRefusesDirectoriesRelativeAndTwoPutsDiffer(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put("x", t.TempDir()); err == nil {
		t.Fatal("directory")
	}
	if _, err := s.Put("x", "relative.txt"); err == nil {
		t.Fatal("relative")
	}
	if _, err := s.Put("bad/../session", "/etc/hosts"); err == nil {
		t.Fatal("session with path separators")
	}
	src := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(src, []byte("1"), 0o644)
	p1, _ := s.Put("x", src)
	os.WriteFile(src, []byte("2"), 0o644)
	p2, _ := s.Put("x", src)
	if p1 == p2 {
		t.Fatal("two puts of a changed file must not collide")
	}
}

func TestGetRefusesPathsOutsideTheStash(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Get("/etc/hosts", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("get must only serve stashed files")
	}
}

func TestEndSessionRemovesItsFiles(t *testing.T) {
	s := New(t.TempDir())
	src := filepath.Join(t.TempDir(), "a.txt")
	os.WriteFile(src, []byte("1"), 0o644)
	p, _ := s.Put("s1", src)
	if err := s.EndSession("s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file should be gone")
	}
}
