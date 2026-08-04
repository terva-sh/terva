package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/testsupport"
)

// writeToolsOverlay drops an operator overlay at $TERVA_HOME/locales/tools/
// en.json and activates it, restoring the default catalog when the test ends.
func writeToolsOverlay(t *testing.T, body string) {
	t.Helper()
	home := testsupport.TempDir(t)
	dir := filepath.Join(home, "locales", "tools")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "en.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := i18n.Configure("en", home); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	t.Cleanup(func() { _ = i18n.Configure("en", testsupport.TempDir(t)) })
}

// TestOperatorCanRetuneAToolDescription is the whole point of the tools
// catalog, tested THROUGH the production caller rather than through i18n.D.
// A tool description is the highest-leverage model-facing text terva has, and
// until this catalog existed the only way to change one was to rebuild the
// binary. If this test stops passing, that capability is gone whatever the
// catalog file says.
func TestOperatorCanRetuneAToolDescription(t *testing.T) {
	writeToolsOverlay(t, `{"tool.read.description":"Read a file. The operator wrote this."}`)

	got := (&ReadTool{}).Description()
	if got != "Read a file. The operator wrote this." {
		t.Errorf("overlay did not reach the tool description:\n got: %q", got)
	}
}

// TestOverlayLeavesOtherDescriptionsAlone: the overlay is per key, so tuning
// one tool must not blank the rest. A merge that replaced the catalog instead
// of layering over it would leave every unlisted tool with an empty
// description, and an empty description is a tool the model stops calling —
// a failure that would look like a model regression, not a config bug.
func TestOverlayLeavesOtherDescriptionsAlone(t *testing.T) {
	writeToolsOverlay(t, `{"tool.read.description":"overridden"}`)

	if d := (&WriteTool{}).Description(); !strings.HasPrefix(d, "Write a file.") {
		t.Errorf("an unrelated tool lost its description: %q", d)
	}
	if d := (&GrepTool{}).Description(); !strings.Contains(d, "regular expression") {
		t.Errorf("an unrelated tool lost its description: %q", d)
	}
}

// TestOverlayKeepsTemplateArguments: bash's description is a %s template
// carrying the resolved shell and the working directory — the one fact a
// relative path depends on. An operator who retunes the wording must still
// get those values filled, or the rewrite silently removes the thing the
// description exists to say.
func TestOverlayKeepsTemplateArguments(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeToolsOverlay(t, `{"tool.bash.description":"Shell: %s. Directory: %s. Operator wording."}`)

	got := (&BashTool{CWD: dir}).Description()
	if !strings.Contains(got, "Shell: "+shellName()+".") {
		t.Errorf("overlay lost the shell argument: %q", got)
	}
	if !strings.Contains(got, "Directory: "+dir+".") {
		t.Errorf("overlay lost the cwd argument: %q", got)
	}
}

// TestNoOverlayKeepsTheShippedEnglish: the default path. Every fresh install
// has no overlay, and the isEN fast path must return the English at the call
// site byte for byte — the catalog must not become a way for the shipped text
// to drift from what the source says.
func TestNoOverlayKeepsTheShippedEnglish(t *testing.T) {
	if err := i18n.Configure("en", testsupport.TempDir(t)); err != nil {
		t.Fatal(err)
	}
	if d := (&ReadTool{}).Description(); !strings.HasPrefix(d, "Read a file from disk.") {
		t.Errorf("no-overlay description is not the shipped English: %q", d)
	}
}

// TestEveryToolDescriptionIsKeyed is the enrolment guard, and it is written
// EMPTY on purpose: it scans the registry rather than listing tools, so a tool
// added tomorrow is covered without anyone remembering this file exists.
//
// The check is behavioural, not textual — it asks whether an overlay can
// actually reach each description, which is the property that matters. A tool
// that returns a bare literal passes every other test in this package and is
// silently unoverridable.
func TestEveryToolDescriptionIsKeyed(t *testing.T) {
	// Key each tool's description to a sentinel: if the description is routed
	// through i18n.D, the overlay wins and we see the sentinel.
	type namedTool interface {
		Name() string
		Description() string
	}
	tools := []namedTool{
		&ReadTool{}, &WriteTool{}, &EditTool{}, &BashTool{}, &GrepTool{}, &GlobTool{},
		&AskUserTool{}, &MemoryTool{}, &SessionInspectTool{}, &SessionSearchTool{},
		&SwarmSpawnTool{}, &RaatiConveneTool{}, &StatusTool{}, &RestartTool{},
		&ArmRestartTool{}, &ActivateToolsTool{}, &GenerateImageTool{}, &ShareFileTool{},
		&DeliverResultTool{}, &ActorSpawnTool{}, &ChatSendImageTool{}, &ChatSendFileTool{},
		&WorktreeListTool{}, &WorktreeCreateTool{}, &WorktreeClaimTool{},
		&WorktreeReleaseTool{}, &WorktreeRemoveTool{},
	}
	var sb strings.Builder
	sb.WriteString("{")
	for i, tl := range tools {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"tool.` + tl.Name() + `.description":"SENTINEL ` + tl.Name() + `"`)
	}
	sb.WriteString("}")
	writeToolsOverlay(t, sb.String())

	for _, tl := range tools {
		want := "SENTINEL " + tl.Name()
		if got := tl.Description(); !strings.HasPrefix(got, want) {
			t.Errorf("%s: description is not routed through i18n.D — an operator cannot retune it.\n"+
				"  wrap it: return i18n.D(%q, <english>)\n  got: %.80q",
				tl.Name(), "tool."+tl.Name()+".description", got)
		}
	}
}
