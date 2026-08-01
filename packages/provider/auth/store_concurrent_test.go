package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// Every write to auth.json is a whole-file read-modify-write, so two writers
// touching DIFFERENT providers still collide: each loads the document, changes
// its own field, and writes all of it back over the other's change. The atomic
// rename is what makes it silent — a reader never sees a torn file, only a
// complete one missing an update.
func TestStoreConcurrentWritersKeepEveryProvider(t *testing.T) {
	s := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))

	providers := []string{"anthropic", "openai", "kimi", "google", "deepseek", "github-copilot"}
	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()
			for range 20 {
				if err := s.SetAPIKey(provider, "key-"+provider); err != nil {
					t.Errorf("SetAPIKey(%s): %v", provider, err)
					return
				}
			}
		}(p)
	}
	wg.Wait()

	c, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, p := range providers {
		got := c.get(p)
		if got == nil || got.APIKey != "key-"+p {
			t.Errorf("%s survived as %+v, want key-%s — an update was lost", p, got, p)
		}
	}
}

// The same collision across PROCESSES, which the in-process mutex cannot see.
// Each child writes its own provider in a loop; all of them must survive.
//
// This is the case that matters in practice: several terva instances share one
// $TERVA_HOME and each refreshes OAuth tokens on its own schedule.
func TestStoreConcurrentProcessesKeepEveryProvider(t *testing.T) {
	if os.Getenv("TERVA_STORE_CHILD_PATH") != "" {
		// Child mode: hammer one provider, then exit.
		s := NewStore(os.Getenv("TERVA_STORE_CHILD_PATH"))
		p := os.Getenv("TERVA_STORE_CHILD_PROVIDER")
		for range 40 {
			if err := s.SetAPIKey(p, "key-"+p); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		os.Exit(0)
	}

	path := filepath.Join(testsupport.TempDir(t), "auth.json")
	providers := []string{"anthropic", "openai", "kimi", "google"}

	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestStoreConcurrentProcessesKeepEveryProvider")
			cmd.Env = append(os.Environ(),
				"TERVA_STORE_CHILD_PATH="+path,
				"TERVA_STORE_CHILD_PROVIDER="+provider,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child %s: %v\n%s", provider, err, out)
			}
		}(p)
	}
	wg.Wait()

	// The file must still parse — a shared fixed temp name let two writers
	// interleave into one truncated file and rename a blend of both.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("auth.json did not survive as valid JSON: %v\n%s", err, raw)
	}
	for _, p := range providers {
		got := c.get(p)
		if got == nil || got.APIKey != "key-"+p {
			t.Errorf("%s survived as %+v, want key-%s — a cross-process update was lost", p, got, p)
		}
	}
}

// The lock lives beside auth.json, never inside it, and no temp file may be
// left behind for the next reader to trip over.
func TestStoreLeavesNoTempFilesAndLocksBeside(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStore(filepath.Join(dir, "auth.json"))
	if err := s.SetOAuth("kimi", OAuthToken{AccessToken: "t"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	// auth.json + auth.json.lock, nothing else.
	if len(names) != 2 {
		t.Errorf("unexpected files in the credential dir: %v", names)
	}
}
