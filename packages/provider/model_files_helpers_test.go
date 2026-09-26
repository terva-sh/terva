package provider

import (
	"errors"
	"fmt"
	"os"

	"terva.sh/terva/packages/privfs"
)

// File I/O around the wire's pure model-file functions, for the tests that
// write a models.json or a cache and read it back. terva's own versions are
// packages/agent/modelfiles; these are the same few lines, kept here so the
// wire's tests do not import the harness.

func loadCache(path string) (ModelCache, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ModelCache{}, nil
	}
	if err != nil {
		return ModelCache{}, err
	}
	return ParseModelCache(b)
}

func loadUserModelsWithWarnings(path string) ([]UserOverride, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	return ParseUserModelsWithWarnings(data)
}

func readUserModelsFile(path string) (UserModelsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return UserModelsFile{Providers: map[string]UserProvider{}}, err
	}
	f, err := ParseUserModelsFile(data)
	if err != nil {
		return f, fmt.Errorf("parse %s: %w", path, err)
	}
	return f, nil
}

func writeUserModelsFile(path string, f UserModelsFile) error {
	b, err := MarshalUserModelsFile(f)
	if err != nil {
		return err
	}
	return privfs.WriteFile(path, b)
}

func findUserModel(path, providerKey, id string) (UserModel, bool, error) {
	f, err := readUserModelsFile(path)
	if err != nil {
		return UserModel{}, false, err
	}
	um, ok := f.Find(providerKey, id)
	return um, ok, nil
}

func upsertUserModel(path, providerKey string, um UserModel) error {
	f, err := readUserModelsFile(path)
	if err != nil {
		return err
	}
	if err := f.Upsert(providerKey, um); err != nil {
		return err
	}
	return writeUserModelsFile(path, f)
}

func removeUserModel(path, providerKey, id string) (bool, error) {
	f, err := readUserModelsFile(path)
	if err != nil {
		return false, err
	}
	if !f.Remove(providerKey, id) {
		return false, nil
	}
	return true, writeUserModelsFile(path, f)
}

func saveCache(path string, c ModelCache) error {
	b, err := MarshalModelCache(c)
	if err != nil {
		return err
	}
	return privfs.WriteFile(path, b)
}
