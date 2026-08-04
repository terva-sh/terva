package secretstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/privfs"
)

// The component registry: who owns a sealed file, and what public key to seal
// it back to.
//
// A component seals its own secrets to two recipients — its own key and
// terva's — so that terva can rotate or audit them while the component is
// stopped, disabled, or uninstalled. That only works if terva knows the
// component's RECIPIENT: re-sealing to terva's new key alone would lock the
// component out of its own file, which is the one outcome dual-recipient exists
// to prevent.
//
// # Why not read it out of the file
//
// Because the file is writable by anything that can write $TERVA_HOME —
// including the model, which reaches it through bash regardless of the write
// jail. A recipient list terva TRUSTED from the file would be an escalation
// from "can write this file" to "can read everything written to it from now
// on": plant a recipient, wait for a rotation, open the ciphertext. So the
// registry is the only sealing input, and it is populated from the connector
// HANDSHAKE, which is authenticated by being a live process terva spawned.
//
// See docs/proposals/secrets-at-rest.md §8.6 and §8.13 Q9.

// RegistryFileName holds the registry, beside the store.
const RegistryFileName = "components.json"

// Component is one registered secret-holding component.
//
// Nothing here is secret: a recipient is a public key and the paths are a
// schema. The file is owner-only because everything in $TERVA_HOME is, not
// because it needs to be.
type Component struct {
	// Name is the component's registered name ("discord-ext").
	Name string `json:"name"`
	// Kind namespaces the name, and becomes the binding scope prefix.
	Kind string `json:"kind"` // "conn" | "ext"
	// Recipient is the component's own age public key.
	Recipient string `json:"recipient"`
	// Paths are the JSON Pointers in File that hold sealed values. Declared,
	// because scanning for the marker can prove what IS sealed but never what
	// SHOULD have been.
	Paths []string `json:"paths,omitempty"`
	// File is the component's state file, relative to the data home.
	File string `json:"file"`
	// LastSeen is when the component last handshaked. Reported, never acted
	// on: reaping a component because it has been quiet is a footgun (a
	// seasonal connector), so forgetting one is explicit.
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// Scope is the binding scope for this component's sealed values.
func (c Component) Scope() string { return c.Kind + ":" + c.Name }

// Registry is the on-disk set of registered components.
type Registry struct {
	path string
	mu   sync.Mutex
}

// NewRegistry returns the registry for a data home.
func NewRegistry(home string) *Registry {
	return &Registry{path: filepath.Join(home, RegistryFileName)}
}

// Path is where the registry lives.
func (r *Registry) Path() string { return r.path }

func (r *Registry) lockPath() string { return r.path + ".lock" }

// Record registers or updates a component, replacing any entry with the same
// kind and name.
//
// A recipient that CHANGES is accepted, not refused: a component may rotate its
// own key while terva is not looking, and the whole point of re-registering at
// every handshake is to learn that. What it costs is that terva can no longer
// re-seal to the old one — which is why a component rotating its key must seal
// to both across its own transition, exactly as terva does.
func (r *Registry) Record(c Component) error {
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Kind) == "" {
		return errors.New("secretstore: component needs a kind and a name")
	}
	if strings.TrimSpace(c.Recipient) == "" {
		return errors.New("secretstore: component needs a recipient")
	}
	if c.File == "" {
		return errors.New("secretstore: component needs a state file")
	}
	if filepath.IsAbs(c.File) {
		return fmt.Errorf("secretstore: component file %q must be relative to the data home", c.File)
	}
	return r.mutate(func(cur *[]Component) {
		for i, existing := range *cur {
			if existing.Kind == c.Kind && existing.Name == c.Name {
				(*cur)[i] = c
				return
			}
		}
		*cur = append(*cur, c)
	})
}

// List returns every registered component, sorted by scope.
func (r *Registry) List() ([]Component, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.loadLocked()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Component) int { return strings.Compare(a.Scope(), b.Scope()) })
	return out, nil
}

// Forget removes a component. This is the explicit reaping Q10 settled on:
// never automatic, because "not seen for N days" is indistinguishable from a
// seasonal connector, and forgetting one that still holds a sealed file means
// terva can no longer rotate it.
func (r *Registry) Forget(kind, name string) error {
	return r.mutate(func(cur *[]Component) {
		*cur = slices.DeleteFunc(*cur, func(c Component) bool {
			return c.Kind == kind && c.Name == name
		})
	})
}

func (r *Registry) mutate(fn func(*[]Component)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lk, err := filelock.Acquire(r.lockPath())
	if err != nil {
		lk = nil // a home that cannot host a lockfile must still register
	}
	defer lk.Release()
	cur, err := r.loadLocked()
	if err != nil {
		return err
	}
	fn(&cur)
	return r.saveLocked(cur)
}

type registryFile struct {
	Version    int         `json:"version"`
	Components []Component `json:"components,omitempty"`
}

func (r *Registry) loadLocked() ([]Component, error) {
	b, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", r.path, err)
	}
	return f.Components, nil
}

func (r *Registry) saveLocked(cs []Component) error {
	slices.SortFunc(cs, func(a, b Component) int { return strings.Compare(a.Scope(), b.Scope()) })
	b, err := json.MarshalIndent(registryFile{Version: formatVersion, Components: cs}, "", "  ")
	if err != nil {
		return err
	}
	if err := privfs.MkdirAll(filepath.Dir(r.path)); err != nil {
		return err
	}
	return privfs.WriteFile(r.path, append(b, '\n'))
}
