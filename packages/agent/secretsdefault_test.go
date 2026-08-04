package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

// A brand-new home configures itself, so a new install's credentials are born
// encrypted rather than migrated later.
func TestFreshHomeGetsAKey(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	initSecretsForFreshHome()

	id, err := config.SecretsIdentity()
	if err != nil {
		t.Fatalf("a fresh home did not get a key: %v", err)
	}
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secrets == nil || cfg.Secrets.Recipient != id.Recipient().String() {
		t.Fatalf("recipient not recorded, or does not match the key: %+v", cfg.Secrets)
	}
	// The payoff: a credential written after this is ciphertext on disk.
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-born-encrypted"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsAgeFile(raw) {
		t.Fatal("auth.json is not encrypted on a home that was set up at first boot")
	}
}

// The other half, and the one that matters more: an EXISTING home is never
// touched. Turning encryption on there rewrites material that is already
// present, which is an explicit act (`terva secret migrate`) precisely because
// it is the operation that can strand credentials.
func TestExistingHomeIsLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{"a home that has logged in", func(t *testing.T, home string) {
			if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-plain"); err != nil {
				t.Fatal(err)
			}
		}},
		{"a home with settings", func(t *testing.T, home string) {
			if err := config.MutateConfig(func(c *config.Config) { c.Model = "some-model" }); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := testsupport.TempDir(t)
			t.Setenv("TERVA_HOME", home)
			tc.setup(t, home)

			initSecretsForFreshHome()

			if _, err := os.Stat(filepath.Join(home, "secrets.key")); !os.IsNotExist(err) {
				t.Fatal("an existing home was encrypted without being asked")
			}
			cfg, err := config.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Secrets != nil {
				t.Fatalf("a recipient was recorded on an existing home: %+v", cfg.Secrets)
			}
		})
	}
}

// A home whose key went missing has ESTABLISHED encryption — ciphertext on
// disk, no key to open it. It reads as "no key configured" to a naive check,
// and minting a fresh one would strand every byte the old key sealed. The
// startup path must be at least as careful as `terva secret init` is.
func TestMissingKeyIsNotMistakenForAFreshHome(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := config.AuthStoreFor().SetAPIKey("deepseek", "sk-strandable"); err != nil {
		t.Fatal(err)
	}
	if err := runSecretInit(os.NewFile(0, os.DevNull)); err != nil {
		t.Fatal(err)
	}
	sealed, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "secrets.key")); err != nil {
		t.Fatal(err)
	}

	initSecretsForFreshHome()

	if _, err := os.Stat(filepath.Join(home, "secrets.key")); err == nil {
		t.Fatal("startup minted a new key over established encryption — the old ciphertext is unrecoverable")
	}
	after, err := os.ReadFile(config.AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(sealed) {
		t.Fatal("the sealed credentials were rewritten")
	}
}
