package cliversion

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/testsupport"
)

func TestInstalledVersionProcesses(t *testing.T) {
	if os.Getenv("TERVA_VERSION_TEST_CHILD") == "1" {
		v := newInstalledVersion("codex", func() (string, error) {
			f, err := os.OpenFile(filepath.Join(os.Getenv("TERVA_HOME"), "probes"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return "", err
			}
			defer f.Close()
			_, err = f.WriteString("probe\n")
			time.Sleep(100 * time.Millisecond)
			return "99.3.4", err
		}, parseCodexVersion)
		_ = v.get()
		waitVersionWorker(t, v)
		if v.get() != "99.3.4" {
			t.Fatal("child missed shared result")
		}
		return
	}
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("TERVA_VERSION_TEST_CHILD", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 6)
	for i := range commands {
		commands[i] = exec.Command(exe, "-test.run=^TestInstalledVersionProcesses$")
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child failed: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "probes"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "probe\n" {
		t.Fatalf("duplicate scans: %q", data)
	}
}

func waitVersionWorker(t *testing.T, v *installedVersion) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v.mu.Lock()
		running := v.running
		v.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("version worker did not finish")
}

func TestInstalledVersionAsyncAndSharedTTL(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	probe := func() (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "99.1.2", nil
	}
	a := newInstalledVersion("claude", probe, parseClaudeVersion)
	result := make(chan string, 1)
	go func() { result <- a.get() }()
	select {
	case got := <-result:
		if got != "" {
			t.Fatalf("initial version = %q, want none until the probe answers", got)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("get blocked on probe")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("probe never started")
	}
	b := newInstalledVersion("claude", probe, parseClaudeVersion)
	_ = b.get()
	for range 20 {
		_ = a.get()
	}
	close(release)
	waitVersionWorker(t, a)
	waitVersionWorker(t, b)
	if a.get() != "99.1.2" || b.get() != "99.1.2" {
		t.Fatal("workers did not propagate detected version")
	}
	c := newInstalledVersion("claude", probe, parseClaudeVersion)
	_ = c.get()
	waitVersionWorker(t, c)
	if c.get() != "99.1.2" || calls.Load() != 1 {
		t.Fatalf("cache miss: version %s, probes %d", c.get(), calls.Load())
	}
}

func TestInstalledVersionStaleCacheAndFailedRefresh(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	path := filepath.Join(home, "cli-versions", "codex.json")
	r := installedVersionRecord{Version: "99.2.3", Checked: time.Now().Add(-2 * installedVersionTTL)}
	data, _ := json.Marshal(r)
	if err := privfs.WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	v := newInstalledVersion("codex", func() (string, error) { close(started); <-release; return "", errors.New("unavailable") }, parseCodexVersion)
	_ = v.get()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("stale cache did not refresh")
	}
	got := v.get()
	close(release)
	waitVersionWorker(t, v)
	if got != "99.2.3" || v.get() != got {
		t.Fatalf("lost cached version: during %s, after %s", got, v.get())
	}
	r = readInstalledVersion(path)
	if r.Version != got || time.Since(r.Checked) >= installedVersionTTL {
		t.Fatalf("failed attempt not cached: %+v", r)
	}
}
