package build

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/secrets"
)

// A brokered secret is sealed in terva's own store, scoped to the extension,
// and never lands in ext-data/ — which is the whole point, since ext-data/ is
// readable by the agent on purpose.
func TestBrokeredSecretIsSealedAndScoped(t *testing.T) {
	home := encryptingHome(t)
	b := extSecretBroker{store: config.SecretStoreIn(home)}

	if err := b.SetSecret("ext:memory", "oauth_token", "tok-live-42"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(config.SecretStorePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if !secrets.IsAgeFile(raw) {
		t.Fatal("the brokered secret is not sealed at rest")
	}
	if strings.Contains(string(raw), "tok-live-42") {
		t.Fatal("the secret is readable on disk")
	}

	got, found, err := b.GetSecret("ext:memory", "oauth_token")
	if err != nil || !found || got != "tok-live-42" {
		t.Fatalf("round trip: %q found=%v err=%v", got, found, err)
	}

	// Another extension's scope sees nothing.
	if _, found, err := b.GetSecret("ext:other", "oauth_token"); err != nil || found {
		t.Errorf("a different scope reached the value: found=%v err=%v", found, err)
	}

	keys, err := b.SecretKeys("ext:memory")
	if err != nil || len(keys) != 1 || keys[0] != "oauth_token" {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	if err := b.DeleteSecret("ext:memory", "oauth_token"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := b.GetSecret("ext:memory", "oauth_token"); found {
		t.Error("the secret survived a delete")
	}
}

// Absent is not an error. An extension asking for a token it has not stored yet
// is the normal first-run path, and returning an error there would push every
// extension into distinguishing "broken" from "not configured" itself.
func TestAbsentBrokeredSecretIsNotAnError(t *testing.T) {
	home := encryptingHome(t)
	b := extSecretBroker{store: config.SecretStoreIn(home)}
	v, found, err := b.GetSecret("ext:memory", "never-set")
	if err != nil {
		t.Fatalf("absent reported as an error: %v", err)
	}
	if found || v != "" {
		t.Fatalf("got %q found=%v", v, found)
	}
	// And a store that cannot be opened IS an error, so the two never merge.
	if err := b.SetSecret("ext:memory", "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, secrets.KeyFileName)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.GetSecret("ext:memory", "k"); err == nil {
		t.Error("an unreadable store reported as merely absent")
	}
}

// Every composition root that wires the session reader must wire the secret
// broker too. They are the same kind of host-side capability injected into the
// same manager, and one wired without the other is a mode where extensions
// silently cannot store credentials — which looks to an extension author like a
// protocol bug rather than a missing line.
//
// Scanned, not listed: a fourth root added later is caught by the same rule.
func TestEveryExtensionRootWiresTheSecretBroker(t *testing.T) {
	dir := ".."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	callSite := regexp.MustCompile(`build\.WireSessionReader\(`)
	broker := regexp.MustCompile(`build\.WireExtensionSecrets\(`)

	var missing []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if callSite.MatchString(src) && !broker.MatchString(src) {
			missing = append(missing, e.Name())
		}
	}
	if len(missing) > 0 {
		t.Errorf("these wire the session reader but not the secret broker: %s\n"+
			"add build.WireExtensionSecrets(extMgr, config.TervaHome()) beside it",
			strings.Join(missing, ", "))
	}
}

// The scope an extension's secrets land under is host-substituted from the
// manifest name; this pins the string the driver builds, so a change on either
// side that silently re-homes every stored secret fails here.
func TestBrokerScopeShape(t *testing.T) {
	home := encryptingHome(t)
	store := config.SecretStoreIn(home)
	b := extSecretBroker{store: store}
	if err := b.SetSecret("ext:memory", "k", "v"); err != nil {
		t.Fatal(err)
	}
	scopes, err := store.Scopes()
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0] != "ext:memory" {
		t.Fatalf("scopes = %v, want [ext:memory]", scopes)
	}
}
