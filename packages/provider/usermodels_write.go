package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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

// FindUserModel returns the raw models.json entry for providerKey/id,
// reporting whether one exists. The editor needs the RAW entry (not the
// merged Model) to tell "explicitly overridden" apart from "inheriting
// the catalog default" on a per-field basis. A missing file yields
// (zero, false, nil).
func FindUserModel(path, providerKey, id string) (UserModel, bool, error) {
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return UserModel{}, false, err
	}
	for _, um := range f.Providers[providerKey].Models {
		if um.ID == id {
			return um, true, nil
		}
	}
	return UserModel{}, false, nil
}

// UpsertUserModel inserts or replaces the entry for um.ID under
// providerKey, preserving every other entry, then writes the file
// atomically. Provider and id are required.
func UpsertUserModel(path, providerKey string, um UserModel) error {
	if providerKey == "" || um.ID == "" {
		return fmt.Errorf("usermodels: provider and model id are required")
	}
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return err
	}
	prov := f.Providers[providerKey]
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
	f.Providers[providerKey] = prov
	return WriteUserModelsFile(path, f)
}

// RemoveUserModel deletes the entry for id under providerKey and writes
// the file atomically, reporting whether an entry was actually removed.
// The provider block is dropped when its last model goes (via
// WriteUserModelsFile's pruning), so a reset leaves no residue.
func RemoveUserModel(path, providerKey, id string) (bool, error) {
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return false, err
	}
	prov, ok := f.Providers[providerKey]
	if !ok {
		return false, nil
	}
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
		return false, nil
	}
	prov.Models = kept
	f.Providers[providerKey] = prov
	if err := WriteUserModelsFile(path, f); err != nil {
		return false, err
	}
	return true, nil
}
