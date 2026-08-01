package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/testsupport"
)

// Offering to install something the loader will then refuse is worse than not
// offering it: the user runs the first-run pack, watches it clone, and gets an
// extension that never starts. Enrols from the pack, so a future supersession
// that forgets to prune it fails here.
func TestTheCorePackOffersNothingSuperseded(t *testing.T) {
	var pack Pack
	if err := json.Unmarshal(corePackJSON, &pack); err != nil {
		t.Fatalf("core pack does not parse: %v", err)
	}
	if len(pack.Extensions) == 0 {
		t.Fatal("core pack is empty; this guard would pass vacuously")
	}
	for _, e := range pack.Extensions {
		if why := extensions.Superseded(e.entryName()); why != "" {
			t.Errorf("core pack offers %q, which the loader refuses: %s", e.entryName(), why)
		}
	}
}

// `ext doctor` must name a superseded extension as superseded rather than
// letting it fall through to "not loaded", which is true, unhelpful, and reads
// exactly like a crash. It must also say the data survives removal — that
// question is what stops people acting on the advice.
func TestExtDoctorExplainsASupersededExtension(t *testing.T) {
	name := ""
	for n := range map[string]bool{"memory": true, "git-worktree": true} {
		if extensions.Superseded(n) != "" {
			name = n
			break
		}
	}
	if name == "" {
		t.Skip("no known superseded extension to exercise")
	}

	var buf bytes.Buffer
	printExtDoctorRow(&buf, extDoctorStaticRow{
		Name: name, Scope: "global", Dir: "/x/extensions/" + name,
		Enabled: true, Exec: "./run.sh",
	}, extdriver.ExtensionDiagnostic{})

	out := buf.String()
	if !strings.Contains(out, "superseded") {
		t.Errorf("doctor did not flag %q as superseded:\n%s", name, out)
	}
	if !strings.Contains(out, "terva ext remove "+name) {
		t.Errorf("doctor did not recommend removal:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join("ext-data", name)) {
		t.Errorf("doctor did not say the extension's data survives removal:\n%s", out)
	}
}

// The doctor's advice is only safe because removal is directory-scoped: it
// deletes $TERVA_HOME/extensions/<name> and leaves ext-data/<name>, which is
// what the built-in's copy-forward reads. Pinned because the advice says so out
// loud, and a future extRemove that "tidied up" the data would turn that line
// into a lie that costs someone their accumulated memories.
func TestExtRemoveLeavesExtensionDataBehind(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	extDir := filepath.Join(home, "extensions", "memory")
	dataDir := filepath.Join(home, "ext-data", "memory")
	for _, d := range []string{extDir, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `{"name":"memory","version":"1.0.0","description":"x","exec":"./run.sh"}`
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dataDir, "user.md")
	if err := os.WriteFile(keep, []byte("- a fact worth keeping\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := extRemove([]string{"memory", "--yes"}); err != nil {
		t.Fatalf("extRemove: %v", err)
	}
	if _, err := os.Stat(extDir); !os.IsNotExist(err) {
		t.Errorf("extension directory survived removal: %v", err)
	}
	b, err := os.ReadFile(keep)
	if err != nil {
		t.Fatalf("extension data was destroyed by removal: %v", err)
	}
	if !strings.Contains(string(b), "worth keeping") {
		t.Errorf("extension data was rewritten: %q", b)
	}
}
