package agent

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The scaffold must be valid, loadable JSON with no warnings, and must
// register both example endpoints under the openai-compatible provider.
// If someone edits modelsScaffold into something the loader rejects,
// `terva models init` would happily write a broken file — this guards it.
func TestModelsInitWritesLoadableScaffold(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	if err := runModelsInit(false); err != nil {
		t.Fatalf("runModelsInit: %v", err)
	}

	path := UserModelsPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("scaffold not written at %s: %v", path, err)
	}

	overrides, warnings := provider.LoadUserModelsWithWarnings(path)
	if len(warnings) != 0 {
		t.Fatalf("scaffold produced warnings: %v", warnings)
	}
	if len(overrides) != 2 {
		t.Fatalf("want 2 scaffold models, got %d", len(overrides))
	}
	for _, o := range overrides {
		if o.Model.Provider != "openai-compatible" {
			t.Errorf("model %q: provider = %q, want openai-compatible", o.Model.ID, o.Model.Provider)
		}
		if o.Model.BaseURL == "" {
			t.Errorf("model %q: empty baseUrl (scaffold should pin each endpoint)", o.Model.ID)
		}
	}
	// The two examples must point at distinct endpoints — that's the
	// whole point of demonstrating multiple endpoints in one file.
	if overrides[0].Model.BaseURL == overrides[1].Model.BaseURL {
		t.Errorf("scaffold endpoints are identical (%q); should show two distinct servers", overrides[0].Model.BaseURL)
	}
}

func TestModelsInitRefusesExistingWithoutForce(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	path := UserModelsPath()
	if err := os.MkdirAll(config.TervaHome(), 0o755); err != nil {
		t.Fatal(err)
	}
	const sentinel = `{"providers":{}}`
	if err := os.WriteFile(path, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runModelsInit(false); err == nil {
		t.Fatal("runModelsInit(false) overwrote an existing file; want refusal error")
	}
	// The pre-existing file must be untouched.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != sentinel {
		t.Fatalf("existing models.json was modified despite no --force: %q", string(got))
	}

	// --force replaces it.
	if err := runModelsInit(true); err != nil {
		t.Fatalf("runModelsInit(true): %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == sentinel {
		t.Fatal("runModelsInit(true) did not overwrite the existing file")
	}
}

func TestRunModelsCommandDispatch(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantHandled bool
		wantErr     bool
	}{
		{"not ours", []string{"--help"}, false, false},
		{"bare models prints help", []string{"models"}, true, false},
		{"models help", []string{"models", "help"}, true, false},
		{"unknown subcommand errors", []string{"models", "bogus"}, true, true},
		{"init unknown flag errors", []string{"models", "init", "--nope"}, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// init paths touch the filesystem; point them somewhere safe.
			t.Setenv("TERVA_HOME", testsupport.TempDir(t))
			handled, err := runModelsCommand(tc.args)
			if handled != tc.wantHandled {
				t.Errorf("handled = %v, want %v", handled, tc.wantHandled)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// captureTierScaffold runs printTierScaffold and returns what it wrote to
// stdout. Reads concurrently so the capture cannot deadlock on a full pipe.
func captureTierScaffold(t *testing.T, providerID string) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	printTierScaffold(providerID)
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// The scaffold exists to be PASTED into config.json, so the guard is that it
// parses and names every rung the ladder actually has — not that it contains
// particular words.
//
// It was a hardcoded "weak/medium/strong" literal, which went stale the day the
// `cheap` rung landed and stayed syntactically perfect the whole time: still
// valid JSON, still pasteable, silently one rung short. A text assertion naming
// the three would have passed too. Parsing it and comparing against the
// ladder's own names is the only check that fails when a rung is added.
func TestTierScaffoldParsesAndNamesEveryRung(t *testing.T) {
	out := captureTierScaffold(t, "ollama")

	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "swarm_tiers") {
			line = strings.TrimSpace(l)
			break
		}
	}
	if line == "" {
		t.Fatalf("no swarm_tiers line in scaffold:\n%s", out)
	}

	// The line is a config FRAGMENT — a key and its value — so it needs the
	// enclosing braces before it is a document.
	var doc struct {
		SwarmTiers map[string]map[string]string `json:"swarm_tiers"`
	}
	if err := json.Unmarshal([]byte("{"+line+"}"), &doc); err != nil {
		t.Fatalf("scaffold is not valid JSON (%v): %s", err, line)
	}

	rungs := doc.SwarmTiers["ollama"]
	if rungs == nil {
		t.Fatalf("scaffold does not key on the provider: %s", line)
	}
	var got []string
	for name := range rungs {
		got = append(got, name)
	}
	want := append([]string(nil), tools.SwarmTierNames()...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("scaffold rungs = %v, ladder has %v", got, want)
	}
}

// A provider with ONLY the cost rung pinned still has a mapping: `tier: cheap`
// resolves there. The header used to be computed from picks[0..2], so such a
// provider was announced as "no tier mapping — `tier` is ignored" and handed a
// scaffold telling the reader to configure what they had already configured.
//
// ollama is the fixture because it ships no built-in ladder, so every rung it
// has comes from the config written here and nothing else can make this pass.
func TestOnlyACheapRungStillCountsAsAMapping(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	cfgJSON := `{"swarm_tiers": {"ollama": {"cheap": {"model": "llama-3.2-1b"}}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := runModelsTiers(false)
	_ = w.Close()
	os.Stdout = old
	out := <-done
	if runErr != nil {
		t.Fatalf("runModelsTiers: %v", runErr)
	}

	var header string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "ollama") {
			header = l
			break
		}
	}
	if header == "" {
		t.Fatalf("ollama not listed:\n%s", out)
	}
	if strings.Contains(header, "no tier mapping") {
		t.Errorf("a pinned cheap rung was reported as no mapping at all: %q", header)
	}
	// And the scaffold that goes with that verdict must not be printed either:
	// it tells the reader to set up what is already set up.
	if strings.Contains(out, `"swarm_tiers": { "ollama"`) {
		t.Errorf("scaffold printed for a provider that already has a rung:\n%s", out)
	}
}
