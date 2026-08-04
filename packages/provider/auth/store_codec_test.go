package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

func testCodec(t *testing.T) (*secrets.Codec, *age.X25519Identity) {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return secrets.NewCodec(func() (*age.X25519Identity, error) { return id, nil }), id
}

func TestStoreCodecEncryptsAtRest(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "auth.json")
	codec, _ := testCodec(t)
	store := NewStoreWithCodec(path, codec)

	if err := store.SetAPIKey("deepseek", "sk-test-123"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsAgeFile(raw) {
		t.Fatalf("auth.json is not encrypted at rest: %q", raw[:min(len(raw), 40)])
	}
	if strings.Contains(string(raw), "sk-test-123") {
		t.Fatal("plaintext key visible in the on-disk file")
	}
	c, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DeepSeek.APIKey != "sk-test-123" {
		t.Fatalf("round trip lost the key: %+v", c.DeepSeek)
	}
}

func TestStoreCodecReadsPlaintextPreMigration(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(`{"deepseek":{"api_key":"sk-plain"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	codec, _ := testCodec(t)
	store := NewStoreWithCodec(path, codec)

	c, err := store.Load()
	if err != nil {
		t.Fatalf("a plaintext file under an enabled codec must still load: %v", err)
	}
	if c.DeepSeek.APIKey != "sk-plain" {
		t.Fatalf("plaintext load mismatch: %+v", c.DeepSeek)
	}
	// The next write migrates the file to ciphertext.
	if err := store.Mutate(func(*Credentials) {}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !secrets.IsAgeFile(raw) {
		t.Fatal("write under an enabled codec left the file plaintext")
	}
}

func TestStoreEncryptedFileWithoutCodecFailsLoudly(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "auth.json")
	codec, _ := testCodec(t)
	if err := NewStoreWithCodec(path, codec).SetAPIKey("deepseek", "sk-x"); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(path).Load(); err == nil {
		t.Fatal("codec-less store read an encrypted file without erroring")
	}

	noKey := secrets.NewCodec(func() (*age.X25519Identity, error) {
		return nil, fmt.Errorf("%w (expected key file at /nope/secrets.key)", secrets.ErrNoKey)
	})
	_, err := NewStoreWithCodec(path, noKey).Load()
	if err == nil {
		t.Fatal("keyless codec read an encrypted file without erroring")
	}
	if !strings.Contains(err.Error(), "/nope/secrets.key") {
		t.Fatalf("error does not say where the key was expected: %v", err)
	}
}

func TestStoreBrokenKeyRefusesPlaintextWrite(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "auth.json")
	broken := secrets.NewCodec(func() (*age.X25519Identity, error) {
		return nil, errors.New("secrets key unreadable")
	})
	store := NewStoreWithCodec(path, broken)
	if err := store.SetAPIKey("deepseek", "sk-x"); err == nil {
		t.Fatal("a broken key configuration must fail the write, not downgrade it to plaintext")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused write still produced a file")
	}
}

func TestStoreCodecWrongKeyNamesTheFile(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "auth.json")
	codec, _ := testCodec(t)
	if err := NewStoreWithCodec(path, codec).SetAPIKey("deepseek", "sk-x"); err != nil {
		t.Fatal(err)
	}
	wrong, _ := testCodec(t)
	_, err := NewStoreWithCodec(path, wrong).Load()
	if err == nil {
		t.Fatal("decrypt with the wrong key succeeded")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error does not name the encrypted file: %v", err)
	}
}
