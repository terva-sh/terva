package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

func TestSecretsIdentityAbsentDefaultIsErrNoKey(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	_, err := SecretsIdentity()
	if !errors.Is(err, secrets.ErrNoKey) {
		t.Fatalf("no key anywhere must report ErrNoKey, got: %v", err)
	}
	if err == nil || !errorContains(err, "secrets.key") {
		t.Fatalf("ErrNoKey does not say where a key was expected: %v", err)
	}
	if SecretsEncryptionConfigured() {
		t.Fatal("encryption reports configured with no key")
	}
}

func TestSecretsIdentityFromKeyFileEnv(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(testsupport.TempDir(t), "elsewhere.key")
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(SecretsKeyFileEnv, keyPath)

	if got := SecretsKeyPath(); got != keyPath {
		t.Fatalf("SecretsKeyPath = %q, want the env-named path %q", got, keyPath)
	}
	loaded, err := SecretsIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Recipient().String() != id.Recipient().String() {
		t.Fatal("loaded identity does not match the key file")
	}
}

func TestSecretsIdentityNamedSourceMissingIsHardError(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv(SecretsKeyFileEnv, filepath.Join(testsupport.TempDir(t), "missing.key"))
	_, err := SecretsIdentity()
	if err == nil {
		t.Fatal("an operator-named key file that is missing must be a hard error")
	}
	if errors.Is(err, secrets.ErrNoKey) {
		t.Fatal("a named-but-missing key must not read as encryption-off")
	}
}

func TestSecretsIdentityDefaultPathAfterInitStyleWrite(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if _, err := SecretsIdentity(); !errors.Is(err, secrets.ErrNoKey) {
		t.Fatal("precondition: no key yet")
	}
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SecretsKeyPath(), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Absence is never memoized: the key created after the first probe is
	// picked up on the next call (this is what lets `terva secret init`
	// encrypt in the same process that just generated the key).
	loaded, err := SecretsIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Recipient().String() != id.Recipient().String() {
		t.Fatal("late-created default key not picked up")
	}
}

func errorContains(err error, s string) bool {
	return err != nil && strings.Contains(err.Error(), s)
}
