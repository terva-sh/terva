package i18n

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/text/language"
)

// resetState returns the package to a known English baseline. Tests are not
// run in parallel because they share the atomic active catalog.
func resetState(t *testing.T) {
	t.Helper()
	if err := Configure("en", ""); err != nil {
		t.Fatalf("Configure(en): %v", err)
	}
	Capture(false)
	missMu.Lock()
	missSingular = map[string]bool{}
	missPlural = map[[2]string]bool{}
	missKeyed = map[string]map[string]string{}
	missMu.Unlock()
}

// TestEnglishIdentity is the load-bearing invariant: with no locale (or
// English) active, every call returns its source byte-for-byte, so the
// default build's output — and the golden/Contains tests — never change.
func TestEnglishIdentity(t *testing.T) {
	resetState(t)
	cases := []string{"start a fresh session", "100% done", "usage: /skill <name>"}
	for _, s := range cases {
		if got := T(s); got != s {
			t.Errorf("T(%q) = %q, want identity", s, got)
		}
	}
	if got := T("saved %d files", 3); got != "saved 3 files" {
		t.Errorf("T with args = %q", got)
	}
	if got := TC("button", "Save"); got != "Save" {
		t.Errorf("TC english = %q, want Save", got)
	}
	if got := TN(1, "%d agent", "%d agents"); got != "1 agent" {
		t.Errorf("TN(1) = %q", got)
	}
	if got := TN(3, "%d agent", "%d agents"); got != "3 agents" {
		t.Errorf("TN(3) = %q", got)
	}
	// A bare '%' with no args must not be run through Sprintf.
	if got := T("progress: 50%"); got != "progress: 50%" {
		t.Errorf("T bare percent = %q", got)
	}
}

func TestEnVariantIsEnglish(t *testing.T) {
	resetState(t)
	if err := Configure("en-US", ""); err != nil {
		t.Fatalf("Configure(en-US): %v", err)
	}
	if got := T("interactive tui"); got != "interactive tui" {
		t.Errorf("en-US should stay english, got %q", got)
	}
}

func TestEmbeddedTranslation(t *testing.T) {
	resetState(t)
	if err := Configure("fi", ""); err != nil {
		t.Fatalf("Configure(fi): %v", err)
	}
	if got := T("interactive tui"); got != "interaktiivinen tui" {
		t.Errorf("T fi = %q, want interaktiivinen tui", got)
	}
	// Missing key degrades gracefully to the English source.
	if got := T("a string with no finnish translation"); got != "a string with no finnish translation" {
		t.Errorf("missing key fallback = %q", got)
	}
	if ActiveLang() != "fi" {
		t.Errorf("ActiveLang = %q, want fi", ActiveLang())
	}
}

// TestTUICatalogMerges proves the split-out tui catalog (locales/tui/) is
// merged into the same Go T lookup as the root catalog: a string routed to tui
// by the //i18n:catalog directive resolves in the active language, and an
// operator's $TERVA_HOME/locales/tui/<lang>.json overlay wins over it.
func TestTUICatalogMerges(t *testing.T) {
	resetState(t)
	if err := Configure("fi", ""); err != nil {
		t.Fatalf("Configure(fi): %v", err)
	}
	// "clear the chat transcript" lives in the tui catalog (a modes slash-cmd
	// description); it must resolve just like a root-catalog string.
	if got := T("clear the chat transcript"); got != "tyhjennä keskusteluloki" {
		t.Errorf("tui embedded fi = %q, want tyhjennä keskusteluloki", got)
	}

	resetState(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "locales", "tui"), 0o755); err != nil {
		t.Fatal(err)
	}
	overlay := `{"clear the chat transcript": "OPERAATTORIN TYHJENNYS"}`
	if err := os.WriteFile(filepath.Join(home, "locales", "tui", "fi.json"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Configure("fi", home); err != nil {
		t.Fatalf("Configure(fi, home): %v", err)
	}
	if got := T("clear the chat transcript"); got != "OPERAATTORIN TYHJENNYS" {
		t.Errorf("tui overlay should win, got %q", got)
	}
}

func TestOverlayOverridesEmbedded(t *testing.T) {
	resetState(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "locales"), 0o755); err != nil {
		t.Fatal(err)
	}
	overlay := `{"interactive tui": "OPERAATTORIN OMA"}`
	if err := os.WriteFile(filepath.Join(home, "locales", "fi.json"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Configure("fi", home); err != nil {
		t.Fatalf("Configure(fi, home): %v", err)
	}
	if got := T("interactive tui"); got != "OPERAATTORIN OMA" {
		t.Errorf("overlay should win, got %q", got)
	}
	// A key only in the embedded file still resolves.
	if got := T("print final text, exit"); got != "tulosta lopullinen teksti ja poistu" {
		t.Errorf("embedded key under overlay = %q", got)
	}
}

// TestCLDRPluralSelection proves we get real CLDR categories for a language
// with more than the English one/other split (Polish: one/few/many/other).
func TestCLDRPluralSelection(t *testing.T) {
	pl := language.Polish
	cases := map[int]string{
		1:  "one",
		2:  "few",
		3:  "few",
		4:  "few",
		5:  "many",
		11: "many",
		22: "few",
		25: "many",
	}
	for n, want := range cases {
		if got := category(pl, n); got != want {
			t.Errorf("category(pl, %d) = %q, want %q", n, got, want)
		}
	}
	if got := category(language.English, 1); got != "one" {
		t.Errorf("english category(1) = %q", got)
	}
	if got := category(language.English, 5); got != "other" {
		t.Errorf("english category(5) = %q", got)
	}
}

func TestFormsFor(t *testing.T) {
	if got := FormsFor(language.English); !equal(got, []string{"one", "other"}) {
		t.Errorf("FormsFor(en) = %v", got)
	}
	if got := FormsFor(language.Polish); !equal(got, []string{"one", "few", "many", "other"}) {
		t.Errorf("FormsFor(pl) = %v", got)
	}
}

func TestTNTranslated(t *testing.T) {
	resetState(t)
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, "locales"), 0o755)
	// Polish plural entry with the three non-"other" forms it needs.
	doc := `{"%d agent|%d agents": {"one": "%d agent", "few": "%d agenty", "many": "%d agentów", "other": "%d agenta"}}`
	_ = os.WriteFile(filepath.Join(home, "locales", "pl.json"), []byte(doc), 0o644)
	if err := Configure("pl", home); err != nil {
		t.Fatalf("Configure(pl): %v", err)
	}
	if got := TN(1, "%d agent", "%d agents"); got != "1 agent" {
		t.Errorf("TN(1) pl = %q", got)
	}
	if got := TN(3, "%d agent", "%d agents"); got != "3 agenty" {
		t.Errorf("TN(3) pl = %q, want 3 agenty (few)", got)
	}
	if got := TN(5, "%d agent", "%d agents"); got != "5 agentów" {
		t.Errorf("TN(5) pl = %q, want 5 agentów (many)", got)
	}
}

func TestVerbs(t *testing.T) {
	cases := map[string][]string{
		"no verbs here":      nil,
		"one %s here":        {"%s"},
		"%d of %d (%0.1f%%)": {"%d", "%d", "%0.1f"},
		"literal %% only":    nil,
	}
	for in, want := range cases {
		if got := Verbs(in); !equal(got, want) {
			t.Errorf("Verbs(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCaptureFlush(t *testing.T) {
	resetState(t)
	home := t.TempDir()
	if err := Configure("fi", home); err != nil {
		t.Fatal(err)
	}
	Capture(true)
	_ = T("interactive tui")            // translated → not captured
	_ = T("brand new untranslated str") // missing → captured
	_ = TN(2, "%d thing", "%d things")  // missing plural → captured
	added, err := Flush(home)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if added != 2 {
		t.Fatalf("added = %d, want 2", added)
	}
	data, err := os.ReadFile(filepath.Join(home, "locales", "fi.todo.json"))
	if err != nil {
		t.Fatalf("read todo: %v", err)
	}
	doc, err := decodeLocale(data)
	if err != nil {
		t.Fatalf("decode todo: %v", err)
	}
	if _, ok := doc.Singular["brand new untranslated str"]; !ok {
		t.Errorf("todo missing singular capture: %v", doc.Singular)
	}
	if _, ok := doc.Plural["%d thing|%d things"]; !ok {
		t.Errorf("todo missing plural capture: %v", doc.Plural)
	}
}

// TestPromptEnglishIdentity: model-facing P() returns its english default
// byte-for-byte in English mode, args applied only when present.
func TestPromptEnglishIdentity(t *testing.T) {
	resetState(t)
	if got := P("study.dir.current", "Read and understand everything in the current directory."); got != "Read and understand everything in the current directory." {
		t.Errorf("P english = %q", got)
	}
	if got := P("study.file", "Read and understand the file %s.", "main.go"); got != "Read and understand the file main.go." {
		t.Errorf("P english with arg = %q", got)
	}
	// A prompt carrying a bare '%' and no args is not run through Sprintf.
	if got := P("x", "budget is 100% spent"); got != "budget is 100% spent" {
		t.Errorf("P bare percent = %q", got)
	}
}

// TestPromptOverlay: an operator's $TERVA_HOME prompts overlay wins over
// the (absent) embedded default, and an unlisted key falls back to the
// english default at the call site.
func TestPromptOverlay(t *testing.T) {
	resetState(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "locales", "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	overlay := `{"study.file": "Lue ja ymmärrä tiedosto %s."}`
	if err := os.WriteFile(filepath.Join(home, "locales", "prompts", "fi.json"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Configure("fi", home); err != nil {
		t.Fatalf("Configure(fi, home): %v", err)
	}
	if got := P("study.file", "Read and understand the file %s.", "main.go"); got != "Lue ja ymmärrä tiedosto main.go." {
		t.Errorf("overlay prompt should win, got %q", got)
	}
	// A key with no override falls through to the english default.
	if got := P("study.dir.current", "Read and understand everything in the current directory."); got != "Read and understand everything in the current directory." {
		t.Errorf("unlisted prompt key fallback = %q", got)
	}
}

// TestPromptCaptureFlush: an untranslated prompt is scaffolded into the
// prompts todo file pre-filled with its english default (not blank).
func TestPromptCaptureFlush(t *testing.T) {
	resetState(t)
	home := t.TempDir()
	if err := Configure("fi", home); err != nil {
		t.Fatal(err)
	}
	Capture(true)
	_ = P("swarm.summary.instruction", "Briefly summarise the collective outcome.")
	added, err := Flush(home)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}
	data, err := os.ReadFile(filepath.Join(home, "locales", "prompts", "fi.todo.json"))
	if err != nil {
		t.Fatalf("read prompts todo: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode prompts todo: %v", err)
	}
	if m["swarm.summary.instruction"] != "Briefly summarise the collective outcome." {
		t.Errorf("prompts todo should pre-fill english, got %q", m["swarm.summary.instruction"])
	}
}

// TestEnglishOverlayOptIn: an English operator can reword UI strings and
// override model-facing prompts via $TERVA_HOME overlays WITHOUT switching
// language. Unoverridden keys still return their English source, and with
// no overlay present English stays the byte-identical fast path.
func TestEnglishOverlayOptIn(t *testing.T) {
	resetState(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "locales", "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Reword a UI string and override a canned prompt — in English.
	if err := os.WriteFile(filepath.Join(home, "locales", "en.json"),
		[]byte(`{"interactive tui": "MY CUSTOM UI"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "locales", "prompts", "en.json"),
		[]byte(`{"study.dir.current": "Study this repo like a detective."}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "locales", "help"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "locales", "help", "en.json"),
		[]byte(`{"help.ext": "CUSTOM EXT HELP"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Configure("en", home); err != nil {
		t.Fatalf("Configure(en, home): %v", err)
	}
	if got := T("interactive tui"); got != "MY CUSTOM UI" {
		t.Errorf("english UI override = %q, want MY CUSTOM UI", got)
	}
	if got := P("study.dir.current", "Read and understand everything in the current directory."); got != "Study this repo like a detective." {
		t.Errorf("english prompt override = %q", got)
	}
	if got := H("help.ext", "terva ext — manage extensions"); got != "CUSTOM EXT HELP" {
		t.Errorf("english help override = %q, want CUSTOM EXT HELP", got)
	}
	// A key with no override still returns its English source.
	if got := T("print final text, exit"); got != "print final text, exit" {
		t.Errorf("unoverridden english key = %q, want identity", got)
	}
}

// TestEnglishNoOverlayIsFastPath: with no overlay files, English is the
// byte-identical fast path even when a home is passed.
func TestEnglishNoOverlayIsFastPath(t *testing.T) {
	resetState(t)
	home := t.TempDir() // exists but has no locales/ overlay
	if err := Configure("en", home); err != nil {
		t.Fatalf("Configure(en, home): %v", err)
	}
	if !current().isEN {
		t.Error("English with no overlay should stay the isEN fast path")
	}
	if got := T("interactive tui"); got != "interactive tui" {
		t.Errorf("T = %q, want identity", got)
	}
}

func TestDecodeLocaleErrors(t *testing.T) {
	if _, err := decodeLocale([]byte(`{"k": 3}`)); err == nil {
		t.Error("numeric value should error")
	}
	if _, err := decodeLocale([]byte(`not json`)); err == nil {
		t.Error("invalid json should error")
	}
}

// TestWebCatalog covers the "web" UI catalog: an English-as-key singular+plural
// catalog in its own subdir, loaded on demand (never resolved in-process) and
// overlaid by $TERVA_HOME like every other catalog.
func TestWebCatalog(t *testing.T) {
	resetState(t)

	// The reference lists every client key; the shipped fi catalog translates them.
	ref, err := ReferenceDocIn(WebCatalogName)
	if err != nil {
		t.Fatalf("ReferenceDocIn(web): %v", err)
	}
	if _, ok := ref.Singular["Message terva…"]; !ok {
		t.Errorf("web reference missing a known key; got %d singular", len(ref.Singular))
	}
	if _, ok := ref.Plural["%d tool call|%d tool calls"]; !ok {
		t.Error("web reference missing the tool-call plural")
	}
	if names := EmbeddedLocaleNamesIn(WebCatalogName); !slices.Contains(names, "fi") {
		t.Errorf("web embedded locales should include fi, got %v", names)
	}

	fi, err := WebCatalog("fi", "")
	if err != nil {
		t.Fatalf("WebCatalog(fi): %v", err)
	}
	if fi.Singular["Message terva…"] != "Viesti tervalle…" {
		t.Errorf("web fi translation wrong: %q", fi.Singular["Message terva…"])
	}
	if fi.Plural["%d tool call|%d tool calls"]["other"] == "" {
		t.Error("web fi plural 'other' form missing")
	}

	// A $TERVA_HOME/locales/web/<lang>.json overlay overrides per key, read fresh.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "locales", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "locales", "web", "fi.json"),
		[]byte(`{"Message terva…":"OVERRIDE"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	over, err := WebCatalog("fi", home)
	if err != nil {
		t.Fatalf("WebCatalog(fi, home): %v", err)
	}
	if over.Singular["Message terva…"] != "OVERRIDE" {
		t.Errorf("overlay not applied: %q", over.Singular["Message terva…"])
	}
	if over.Singular["Send"] != "Lähetä" {
		t.Errorf("non-overridden key should keep embedded fi: %q", over.Singular["Send"])
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
