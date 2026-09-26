// Package cliversion finds the installed Claude Code and Codex CLI versions
// that terva's client identities claim, by running each CLI with --version in
// the background and sharing the result across terva processes through a
// cache under TERVA_HOME. The wire (packages/provider) does neither: it takes
// the result through provider.WithClaudeCodeVersion and
// provider.WithCodexCLIVersion, and keeps its compiled baseline as the floor.
// Decision 0021, rule 5.
package cliversion

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/privfs"
)

const installedVersionTTL = 10 * time.Minute

type installedVersionRecord struct {
	Version string    `json:"version"`
	Checked time.Time `json:"checked"`
}

type installedVersion struct {
	mu          sync.Mutex
	name, value string
	next        time.Time
	running     bool
	probe       func() (string, error)
	parse       func(string) (string, bool)
}

func newInstalledVersion(name string, probe func() (string, error), parse func(string) (string, bool)) *installedVersion {
	return &installedVersion{name: name, probe: probe, parse: parse}
}

// get never waits for disk access, a subprocess, or another process's lock.
// The first call returns "" while the worker loads the disk cache, and the wire
// claims its baseline until a version arrives.
func (v *installedVersion) get() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.running && !time.Now().Before(v.next) {
		v.running = true
		go v.refresh()
	}
	return v.value
}

func (v *installedVersion) publish(r installedVersionRecord) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.value = r.Version
}

func readInstalledVersion(path string) installedVersionRecord {
	var r installedVersionRecord
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, &r) != nil {
			return installedVersionRecord{}
		}
	}
	return r
}

func (v *installedVersion) refresh() {
	next := time.Now().Add(installedVersionTTL)
	defer func() {
		v.mu.Lock()
		v.next, v.running = next, false
		v.mu.Unlock()
	}()
	path := filepath.Join(envcompat.Home(), "cli-versions", v.name+".json")
	r := readInstalledVersion(path)
	v.publish(r)
	// All waits stay in this goroutine. Re-read under the OS lock so a
	// concurrent launch uses the winner's result instead of probing again.
	lock, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return
	}
	defer lock.Release()
	r = readInstalledVersion(path)
	v.publish(r)
	now := time.Now()
	if !r.Checked.After(now) && now.Sub(r.Checked) < installedVersionTTL {
		next = r.Checked.Add(installedVersionTTL)
		return
	}
	// Persist the attempt before exec. A killed process or failed probe must
	// not cause every subsequent launch to retry immediately.
	r.Checked = now
	if data, err := json.Marshal(r); err == nil {
		if privfs.WriteFile(path, data) != nil {
			return
		}
	}
	if out, err := v.probe(); err == nil {
		if parsed, ok := v.parse(out); ok {
			r.Version = parsed
		}
	}
	v.publish(r)
	if data, err := json.Marshal(r); err == nil {
		_ = privfs.WriteFile(path, data)
	}
	next = r.Checked.Add(installedVersionTTL)
}
