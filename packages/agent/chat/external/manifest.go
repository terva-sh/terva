// Package external runs chat connectors as separate executables: a
// proxy chat.Connector spawns the child process and speaks the
// connproto JSON-lines protocol over its stdio, so out-of-process
// connectors register into the same chat.Service registry as
// compiled-in ones and are indistinguishable to the loop, the
// bridge, the TUI, and `terva bot`.
//
// Discovery is GLOBAL ONLY ($TERVA_HOME/connectors/<name>/connector.json),
// never project-local: a cloned repository must not be able to
// register an executable that receives the user's private chats. The
// per-invocation dev affordance is `--connector-manifest PATH`, which
// loads exactly the named manifest and persists nothing.
package external

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Manifest is the connector.json file describing one external
// connector: how to launch it plus display metadata. Same shape as
// the extension manifest, minus extension-specific fields.
type Manifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Exec        string   `json:"exec"`              // executable; see resolveExec
	Args        []string `json:"args,omitempty"`    // argv prefix; the verb (run/setup/...) is appended last
	Enabled     *bool    `json:"enabled,omitempty"` // nil = enabled
	Description string   `json:"description,omitempty"`
}

// IsEnabled returns the manifest's effective enabled state (default
// true, mirroring extensions).
func (m Manifest) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// ConnectorsDir is where installed/linked connector manifests live.
func ConnectorsDir(tervaHome string) string {
	return filepath.Join(tervaHome, "connectors")
}

// ManifestPath returns the canonical manifest location for name.
func ManifestPath(tervaHome, name string) string {
	return filepath.Join(ConnectorsDir(tervaHome), name, "connector.json")
}

// LoadManifest reads and validates a manifest. It resolves symlinks
// first (a linked dev manifest points at the working copy) and
// returns the REAL manifest directory, so a relative exec resolves
// next to the file the author edits, not next to the symlink.
func LoadManifest(path string) (Manifest, string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("resolve manifest: %w", err)
	}
	raw, err := os.ReadFile(real)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, "", fmt.Errorf("parse manifest: %w", err)
	}
	if err := validateName(m.Name); err != nil {
		return Manifest{}, "", err
	}
	if m.Exec == "" {
		return Manifest{}, "", fmt.Errorf("manifest: exec is required")
	}
	dir, err := filepath.Abs(filepath.Dir(real))
	if err != nil {
		return Manifest{}, "", err
	}
	return m, dir, nil
}

// validateName rejects names unusable as a path component: they name
// the state dir ($TERVA_HOME/connectors/<name>/) and the log file.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("manifest: name is required")
	}
	if name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, filepath.Separator) {
		return fmt.Errorf("manifest: invalid name %q", name)
	}
	return nil
}

// resolveExec applies the same resolution rules as extension
// manifests: absolute paths as-is, "./"-style and other relative
// paths against the manifest dir, bare names via $PATH.
func resolveExec(execPath, dir string) string {
	switch {
	case filepath.IsAbs(execPath):
		return execPath
	case strings.HasPrefix(execPath, "."+string(filepath.Separator)) ||
		strings.HasPrefix(execPath, ".."+string(filepath.Separator)) ||
		execPath == "." || execPath == "..":
		return filepath.Join(dir, execPath)
	case strings.ContainsRune(execPath, filepath.Separator):
		return filepath.Join(dir, execPath)
	default:
		// bare name: left for exec.LookPath via exec.Command.
		return execPath
	}
}

// Link installs manifestPath as $TERVA_HOME/connectors/<name>/connector.json
// via a symlink — the persistent flavor of the dev affordance: a
// one-time explicit install verb that leaves a visible, auditable
// artifact (`terva bot status` lists it, `terva bot reset` removes it)
// instead of ambient discovery. Returns the created link path.
func Link(tervaHome, manifestPath string) (string, error) {
	abs, err := filepath.Abs(manifestPath)
	if err != nil {
		return "", err
	}
	m, _, err := LoadManifest(abs)
	if err != nil {
		return "", err
	}
	dst := ManifestPath(tervaHome, m.Name)
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("connector %q already installed at %s (run `terva bot reset --connector %s` first)", m.Name, dst, m.Name)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Symlink(abs, dst); err != nil {
		return "", fmt.Errorf("link manifest: %w (copying the file there works too)", err)
	}
	return dst, nil
}

// tervaVersion is reported to children in hello_ack. Set once at CLI
// startup; "" (a dev binary that skipped the setter) is harmless.
var (
	tervaVersionMu sync.Mutex
	tervaVersion   string
)

// SetTervaVersion records the running binary's version for hello_ack.
func SetTervaVersion(v string) {
	tervaVersionMu.Lock()
	tervaVersion = v
	tervaVersionMu.Unlock()
}

func getTervaVersion() string {
	tervaVersionMu.Lock()
	defer tervaVersionMu.Unlock()
	return tervaVersion
}
