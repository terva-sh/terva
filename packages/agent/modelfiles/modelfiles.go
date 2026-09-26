// Package modelfiles reads and writes terva's model files: the discovered-models
// cache and the user's models.json. The wire (packages/provider) parses and
// encodes them and does no I/O (decision 0021, rule 5); this package is the
// file half, with the names and signatures the wire used to export.
package modelfiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// LoadCache reads the model cache from path. Returns an empty ModelCache
// (no error) if the file does not exist.
func LoadCache(path string) (provider.ModelCache, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return provider.ModelCache{}, nil
	}
	if err != nil {
		return provider.ModelCache{}, err
	}
	return provider.ParseModelCache(b)
}

// SaveCache writes the cache atomically.
func SaveCache(path string, c provider.ModelCache) error {
	b, err := provider.MarshalModelCache(c)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// LoadUserModelsWithWarnings reads a models.json file and parses it with
// provider.ParseUserModelsWithWarnings. A file that cannot be read yields
// nothing and no warning, as a missing file should.
func LoadUserModelsWithWarnings(path string) ([]provider.UserOverride, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	return provider.ParseUserModelsWithWarnings(data)
}

// ReadUserModelsFile reads and parses a models.json file. A missing or
// empty file is not an error: it returns a file with a ready-to-use
// (non-nil) Providers map. A malformed file IS an error, so a caller
// that's about to rewrite the file never silently clobbers content it
// couldn't understand.
func ReadUserModelsFile(path string) (provider.UserModelsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return provider.UserModelsFile{Providers: map[string]provider.UserProvider{}}, err
	}
	f, err := provider.ParseUserModelsFile(data)
	if err != nil {
		return f, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}

// WriteUserModelsFile writes f to path atomically (temp + rename),
// pretty-printed with a trailing newline and with empty provider blocks
// pruned (see provider.MarshalUserModelsFile).
func WriteUserModelsFile(path string, f provider.UserModelsFile) error {
	b, err := provider.MarshalUserModelsFile(f)
	if err != nil {
		return err
	}
	return writeAtomic(path, b)
}

// FindUserModel returns the raw models.json entry for providerKey/id,
// reporting whether one exists (see provider.UserModelsFile.Find). A missing
// file yields (zero, false, nil).
func FindUserModel(path, providerKey, id string) (provider.UserModel, bool, error) {
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return provider.UserModel{}, false, err
	}
	um, ok := f.Find(providerKey, id)
	return um, ok, nil
}

// UpsertUserModel inserts or replaces the entry for um.ID under providerKey
// (see provider.UserModelsFile.Upsert), then writes the file atomically.
func UpsertUserModel(path, providerKey string, um provider.UserModel) error {
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return err
	}
	if err := f.Upsert(providerKey, um); err != nil {
		return err
	}
	return WriteUserModelsFile(path, f)
}

// RemoveUserModel deletes the entry for id under providerKey (see
// provider.UserModelsFile.Remove) and writes the file atomically, reporting
// whether an entry was actually removed. Nothing is written when none was.
func RemoveUserModel(path, providerKey, id string) (bool, error) {
	f, err := ReadUserModelsFile(path)
	if err != nil {
		return false, err
	}
	if !f.Remove(providerKey, id) {
		return false, nil
	}
	if err := WriteUserModelsFile(path, f); err != nil {
		return false, err
	}
	return true, nil
}

// writeAtomic writes b to path through a temporary file and a rename, so a
// reader never sees half a file. The directory is created private.
func writeAtomic(path string, b []byte) error {
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
