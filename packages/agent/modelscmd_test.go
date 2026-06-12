package agent

import (
	"os"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The scaffold must be valid, loadable JSON with no warnings, and must
// register both example endpoints under the openai-compatible provider.
// If someone edits modelsScaffold into something the loader rejects,
// `terva models init` would happily write a broken file — this guards it.
func TestModelsInitWritesLoadableScaffold(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())

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
	t.Setenv("TERVA_HOME", t.TempDir())
	path := UserModelsPath()
	if err := os.MkdirAll(TervaHome(), 0o755); err != nil {
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
			t.Setenv("TERVA_HOME", t.TempDir())
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
