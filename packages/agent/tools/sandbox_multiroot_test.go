package tools

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestSandboxPeerRoots(t *testing.T) {
	primary, peer, outside := testsupport.TempDir(t), testsupport.TempDir(t), testsupport.TempDir(t)
	s := NewSandbox(primary)
	s.Lock()
	if err := s.CheckPath(filepath.Join(peer, "new", "file")); err == nil {
		t.Fatal("positive control: peer is writable before grant")
	}
	if err := s.AddRoot(peer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoot(peer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoot(primary); err != nil {
		t.Fatal(err)
	}
	if len(s.Roots()) != 2 {
		t.Fatal("duplicate roots")
	}
	roots := s.Roots()
	roots[1] = outside
	for _, root := range []string{primary, peer} {
		if err := s.CheckPath(filepath.Join(root, "new", "file")); err != nil {
			t.Fatal(err)
		}
		if err := s.CheckCommand("cd \"" + root + "\" && pwd"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CheckPath(filepath.Join(outside, "file")); err == nil {
		t.Fatal("write escaped roots")
	}
	if err := s.CheckCommand("cd \"" + outside + "\""); err == nil {
		t.Fatal("cd escaped roots")
	}
	if err := s.CheckPathRead(filepath.Join(outside, "file")); err != nil {
		t.Fatal("read policy changed")
	}
	s.AddSecretRoot(peer)
	if err := s.CheckPathRead(filepath.Join(peer, "secret")); err == nil {
		t.Fatal("peer grant lifted secret denial")
	}
	if err := s.RemoveRoot(primary); err == nil {
		t.Fatal("primary was removable")
	}
	if err := s.RemoveRoot(peer); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPath(filepath.Join(peer, "file")); err == nil {
		t.Fatal("revocation did not hold")
	}
	s.Unlock()
	if err := s.CheckPath(filepath.Join(outside, "file")); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxPeerSymlinks(t *testing.T) {
	primary, peer, outside := testsupport.TempDir(t), testsupport.TempDir(t), testsupport.TempDir(t)
	s := NewSandbox(primary)
	s.Lock()
	alias := filepath.Join(primary, "alias")
	if err := os.Symlink(peer, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := s.AddRoot(alias); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoot(peer); err != nil {
		t.Fatal(err)
	}
	if len(s.Roots()) != 2 {
		t.Fatal("symlink alias was not deduplicated")
	}
	if err := s.CheckPath(filepath.Join(alias, "new", "file")); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(peer, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPath(filepath.Join(escape, "new", "file")); err == nil {
		t.Fatal("symlink escaped peer")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPath(filepath.Join(alias, "file")); err == nil {
		t.Fatal("retargeted alias widened authority")
	}
}

func TestSandboxRootValidationAndReroot(t *testing.T) {
	primary, peer := testsupport.TempDir(t), testsupport.TempDir(t)
	s := NewSandbox(primary)
	s.Lock()
	for _, path := range []string{"", filepath.Join(peer, "missing")} {
		if err := s.AddRoot(path); err == nil {
			t.Fatal("invalid root accepted")
		}
	}
	file := filepath.Join(peer, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRoot(file); err == nil {
		t.Fatal("file root accepted")
	}
	if err := s.AddRoot(peer); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrimaryRoot(peer); err != nil {
		t.Fatal(err)
	}
	if !s.Locked() || len(s.Roots()) != 1 {
		t.Fatal("reroot changed lock or retained duplicate")
	}
	if err := s.CheckPath(filepath.Join(primary, "file")); err == nil {
		t.Fatal("old primary retained authority")
	}
}

func TestSandboxPeerRootConcurrency(t *testing.T) {
	primary, peer := testsupport.TempDir(t), testsupport.TempDir(t)
	s := NewSandbox(primary)
	s.Lock()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.AddRoot(peer)
				_ = s.CheckPath(filepath.Join(peer, "file"))
				_ = s.CheckCommand("cd \"" + peer + "\"")
				_ = s.DisplayPath(filepath.Join(primary, "file"), "file")
				_ = s.Roots()
				_ = s.SetPrimaryRoot(primary)
				_ = s.RemoveRoot(peer)
			}
		}()
	}
	wg.Wait()
}
