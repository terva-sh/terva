package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"terva.sh/terva/packages/privfs"
)

// This file is the write side of $TERVA_HOME/models.json. The read
// side (LoadUserModelsWithWarnings) parses the file into the active
// catalog's user layer; these helpers let the TUI model editor persist
// a single model's overrides without disturbing the rest of the file.

// ReadUserModelsFile reads and parses a models.json file. A missing or
// empty file is not an error: it returns a file with a ready-to-use
// (non-nil) Providers map. A malformed file IS an error, so a caller
// that's about to rewrite the file never silently clobbers content it
// couldn't understand.
func ReadUserModelsFile(path string) (UserModelsFile, error) {
	empty := UserModelsFile{Providers: map[string]UserProvider{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, err
	}
	if len(data) == 0 {
		return empty, nil
	}
	var f UserModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return empty, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Providers == nil {
		f.Providers = map[string]UserProvider{}
	}
	return f, nil
}

// WriteUserModelsFile writes f to path atomically (temp + rename),
// pretty-printed with a trailing newline. Provider blocks that hold no
// models are pruned first, so removing a provider's last override never
// leaves an empty husk behind.
func WriteUserModelsFile(path string, f UserModelsFile) error {
	if f.Providers == nil {
		f.Providers = map[string]UserProvider{}
	}
	for name, prov := range f.Providers {
		if len(prov.Models) == 0 {
			delete(f.Providers, name)
		}
	}
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// userModelsProviderKeysFor returns every key in f that names the same provider
// as providerKey once legacy aliases are resolved — the canonical spelling
// first, then any legacy spelling in sorted order.
//
// The read door and the write door were keyed differently for the same model.
// LoadUserModelsWithWarnings resolves "kimi-code" to "kimi" before building the
// Model, so an operator's legacy-keyed override IS live and shows up in the
// picker; these three functions indexed f.Providers by the raw key, and every
// caller reaches them holding the CANONICAL provider off the merged catalog
// (FindModel matches Model.Provider exactly, so a client cannot even name the
// legacy spelling). The result was a settings form that reported no override
// while one was in force, and a Reset that removed nothing and reported success.
//
// Sorted, and canonical first, so a file holding both spellings resolves the
// same way on every run — Go map order otherwise decides which entry the editor
// shows.
func userModelsProviderKeysFor(f UserModelsFile, providerKey string) []string {
	canonical := NormalizeUserModelProviderKey(providerKey)
	var legacy []string
	for key := range f.Providers {
		if key == canonical || NormalizeUserModelProviderKey(key) != canonical {
			continue
		}
		legacy = append(legacy, key)
	}
	sort.Strings(legacy)
	return append([]string{canonical}, legacy...)
}

// FindUserModel returns the raw models.json entry for providerKey/id,
// reporting whether one exists. The editor needs the RAW entry (not the
// merged Model) to tell "explicitly overridden" apart from "inheriting
// the catalog default" on a per-field basis. A missing file yields
// (zero, false, nil).
//
// providerKey is the canonical provider; an entry filed under a legacy
// spelling of it is found too, because the loader treats that entry as live.
func FindUserModel(path, providerKey, id string) (UserModel, bool, error) {
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return UserModel{}, false, err
	}
	for _, key := range userModelsProviderKeysFor(f, providerKey) {
		for _, um := range f.Providers[key].Models {
			if um.ID == id {
				return um, true, nil
			}
		}
	}
	return UserModel{}, false, nil
}

// UpsertUserModel inserts or replaces the entry for um.ID under
// providerKey, preserving every other entry, then writes the file
// atomically. Provider and id are required.
//
// The entry always lands under the CANONICAL provider key, and the same id is
// dropped from every legacy-spelled block on the way. Without that fold, saving
// against a legacy-keyed file leaves two entries for one model, which the
// loader applies in map order — so any field set by both flips between runs.
func UpsertUserModel(path, providerKey string, um UserModel) error {
	if providerKey == "" || um.ID == "" {
		return fmt.Errorf("usermodels: provider and model id are required")
	}
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return err
	}
	keys := userModelsProviderKeysFor(f, providerKey)
	canonical := keys[0]
	for _, key := range keys[1:] {
		if block, dropped := userProviderWithout(f.Providers[key], um.ID); dropped {
			f.Providers[key] = block
		}
	}
	prov := f.Providers[canonical]
	replaced := false
	for i := range prov.Models {
		if prov.Models[i].ID == um.ID {
			prov.Models[i] = um
			replaced = true
			break
		}
	}
	if !replaced {
		prov.Models = append(prov.Models, um)
	}
	f.Providers[canonical] = prov
	return WriteUserModelsFile(path, f)
}

// userProviderWithout returns prov with any entry for id removed, and whether
// one was there.
func userProviderWithout(prov UserProvider, id string) (UserProvider, bool) {
	kept := make([]UserModel, 0, len(prov.Models))
	removed := false
	for _, um := range prov.Models {
		if um.ID == id {
			removed = true
			continue
		}
		kept = append(kept, um)
	}
	if !removed {
		return prov, false
	}
	prov.Models = kept
	return prov, true
}

// RemoveUserModel deletes the entry for id under providerKey and writes
// the file atomically, reporting whether an entry was actually removed.
// The provider block is dropped when its last model goes (via
// WriteUserModelsFile's pruning), so a reset leaves no residue.
//
// Every spelling of the provider is cleared, not just the canonical one. This
// is the Reset button: leaving a legacy-keyed entry behind would report success
// and leave the override in force, which is exactly what it used to do.
func RemoveUserModel(path, providerKey, id string) (bool, error) {
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return false, err
	}
	removed := false
	for _, key := range userModelsProviderKeysFor(f, providerKey) {
		prov, ok := f.Providers[key]
		if !ok {
			continue
		}
		block, dropped := userProviderWithout(prov, id)
		if !dropped {
			continue
		}
		f.Providers[key] = block
		removed = true
	}
	if !removed {
		return false, nil
	}
	if err := WriteUserModelsFile(path, f); err != nil {
		return false, err
	}
	return true, nil
}
