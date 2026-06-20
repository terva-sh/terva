package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// installDummyExt drops a minimal installed extension into the global dir.
func installDummyExt(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(`{"name":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalExtensionsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	if !globalExtensionsEmpty() {
		t.Error("missing dir should count as empty")
	}
	if err := os.MkdirAll(filepath.Join(home, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !globalExtensionsEmpty() {
		t.Error("empty dir should count as empty")
	}
	installDummyExt(t, home, "demoext")
	if globalExtensionsEmpty() {
		t.Error("dir with an installed extension should not count as empty")
	}
}

func TestCorePackOfferSentinel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	if corePackAlreadyOffered() {
		t.Error("fresh home should not be marked offered")
	}
	if err := markCorePackOffered(); err != nil {
		t.Fatalf("markCorePackOffered: %v", err)
	}
	if !corePackAlreadyOffered() {
		t.Error("expected offered after mark")
	}
}

func TestCorePackOfferDisabledByConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	if corePackOfferDisabled() {
		t.Error("no config should mean not disabled")
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"disable_core_pack_offer":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !corePackOfferDisabled() {
		t.Error("config opt-out should disable the offer")
	}
}

// corePackOfferAllowed is the gate minus the TTY check. Exercise each
// short-circuit.
func TestCorePackOfferAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	// Fresh, empty home, no flags -> allowed.
	if !corePackOfferAllowed(Args{}) {
		t.Fatal("fresh empty home should allow the offer")
	}

	// --no-ext suppresses it.
	if corePackOfferAllowed(Args{NoExt: true}) {
		t.Error("--no-ext should suppress the offer")
	}

	// Already offered (sentinel) suppresses it.
	if err := markCorePackOffered(); err != nil {
		t.Fatal(err)
	}
	if corePackOfferAllowed(Args{}) {
		t.Error("already-offered should suppress the offer")
	}
}

func TestCorePackOfferAllowedWhenInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)
	installDummyExt(t, home, "demoext")

	if corePackOfferAllowed(Args{}) {
		t.Error("an installed extension should suppress the offer")
	}
}

// maybeOfferCorePack must be a no-op under a non-TTY stdin (the test
// harness): no prompt, no install, no sentinel written.
func TestMaybeOfferCorePackNonTTYNoop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	maybeOfferCorePack(Args{})

	if corePackAlreadyOffered() {
		t.Error("non-TTY run must not mark offered")
	}
	if !globalExtensionsEmpty() {
		t.Error("non-TTY run must not install anything")
	}
}
