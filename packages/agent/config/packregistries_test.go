package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// PackRegistries exempts a host from the egress guard's address policy, so a
// cloned repository able to set it would hand that project the private
// network. ProjectConfig carries no counterpart key and ResolveConfig names no
// override, which makes it user-only by construction. Construction is not a
// guarantee until something checks it.
func TestPackRegistriesIsUserLayerOnly(t *testing.T) {
	// Structural half. If somebody adds the key to ProjectConfig, this fails
	// before any merge logic is even reached.
	if _, found := jsonTags(t, reflect.TypeOf(ProjectConfig{}))["pack_registries"]; found {
		t.Fatal("ProjectConfig declares pack_registries; a project must never name a host that the egress guard will then dial")
	}

	// Behavioural half. A project that writes the key anyway gets nothing,
	// trusted or not, so the guarantee does not rest on the struct alone.
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := testsupport.TempDir(t)
	writeProjectConfig(t, cwd, `{"pack_registries":["http://169.254.169.254"]}`)

	for _, trusted := range []bool{false, true} {
		if got := ResolveConfig(cwd, trusted).Config.PackRegistries; len(got) != 0 {
			t.Fatalf("trusted=%v: project config set pack_registries %v; a cloned repo must not be able to open the private network", trusted, got)
		}
	}
}

// The other half: the user's own pack_registries DOES survive resolution, so
// the guarantee above reads "a project cannot" rather than "the key does
// nothing". Without this, deleting the field would pass the test above.
func TestUserPackRegistriesSurvivesResolve(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"),
		[]byte(`{"pack_registries":["https://packs.internal.corp"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := testsupport.TempDir(t)

	got := ResolveConfig(cwd, true).Config.PackRegistries
	if len(got) != 1 || got[0] != "https://packs.internal.corp" {
		t.Fatalf("the user's own pack_registries did not survive resolution: %v", got)
	}
}
