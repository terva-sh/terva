package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func statePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(testsupport.TempDir(t), "20260101-120000-aaaaaaaa.state.json")
}

func TestSessionStatePathSitsBesideTheTranscript(t *testing.T) {
	if got := SessionStatePathFor(""); got != "" {
		t.Errorf("SessionStatePathFor(\"\") = %q, want empty", got)
	}
	got := SessionStatePathFor("/s/20260101-120000-aaaaaaaa.jsonl")
	want := "/s/20260101-120000-aaaaaaaa.state.json"
	if got != want {
		t.Errorf("SessionStatePathFor = %q, want %q", got, want)
	}
	// It must be one of the sidecars the lifecycle carries, or a draft outlives
	// the session it belongs to.
	var found bool
	for _, p := range SessionSidecarPaths("/s/20260101-120000-aaaaaaaa.jsonl") {
		if p == want {
			found = true
		}
	}
	if !found {
		t.Error("the state sidecar is not in SessionSidecarPaths, so delete/prune/archive will not carry it")
	}
}

func TestAComposerDraftSurvivesARoundTrip(t *testing.T) {
	path := statePath(t)
	var st SessionState
	if err := st.SetComposer(ComposerDraft{Text: "the half-written message"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionState(path, st); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}

	got, ok := LoadSessionState(path).Composer()
	if !ok {
		t.Fatal("no composer draft came back")
	}
	if got.Text != "the half-written message" {
		t.Errorf("text = %q, want %q", got.Text, "the half-written message")
	}
	// Source defaults to the user: an untagged draft is the user's writing, and
	// defaulting the other way would hand them the machine's words as their own.
	if got.Source != ComposerSourceUser {
		t.Errorf("source = %q, want %q", got.Source, ComposerSourceUser)
	}
	if got.IsSuggestion() {
		t.Error("an untagged draft reported itself a suggestion")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("updated_at was not stamped")
	}
}

// The provenance tag is the whole reason a suggestion may be persisted at all:
// it comes back as an offer, not as the user's text.
func TestAStoredSuggestionStaysASuggestion(t *testing.T) {
	path := statePath(t)
	var st SessionState
	if err := st.SetComposer(ComposerDraft{Text: "run the tests", Source: ComposerSourceSuggestion}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionState(path, st); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadSessionState(path).Composer()
	if !ok {
		t.Fatal("no draft came back")
	}
	if !got.IsSuggestion() {
		t.Fatalf("source = %q: a stored suggestion came back as the user's own writing, "+
			"which is exactly what the source tag exists to prevent", got.Source)
	}
}

// An older binary must not delete a newer one's tenant by round-tripping the
// file. This is the guard that makes the sidecar general-purpose rather than a
// drafts file with delusions.
func TestUnknownTenantsSurviveARoundTrip(t *testing.T) {
	path := statePath(t)
	seeded := `{"version":1,"composer":{"text":"mine","source":"user"},` +
		`"from_the_future":{"setting":42},"another":["a","b"]}`
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}

	st := LoadSessionState(path)
	if err := st.SetComposer(ComposerDraft{Text: "replaced"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionState(path, st); err != nil {
		t.Fatal(err)
	}

	var out map[string]json.RawMessage
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"from_the_future", "another"} {
		if _, ok := out[key]; !ok {
			t.Errorf("tenant %q was dropped: an older binary just destroyed state it did not understand", key)
		}
	}
	if got := string(out["another"]); got != `["a","b"]` {
		t.Errorf("tenant \"another\" = %s, want it carried verbatim", got)
	}
}

// A sidecar is a convenience and the transcript is the data. Every unreadable
// form means "no state" — never an error a caller could propagate into a failed
// session open.
func TestAnUnreadableSidecarMeansNoStateNotAnError(t *testing.T) {
	dir := testsupport.TempDir(t)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"truncated", `{"composer":{"text":"half`},
		{"garbage", "this is not json at all"},
		{"empty file", ""},
		{"a list, not an object", `["wrong","shape"]`},
		{"composer of the wrong type", `{"composer":"a string, not an object"}`},
		{"future version", `{"version":99999,"composer":{"text":"x"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+".state.json")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			// The contract is the signature: no error to propagate.
			st := LoadSessionState(p)
			if _, ok := st.Composer(); ok && tc.name != "future version" {
				t.Errorf("%s produced a draft; it should have been treated as no state", tc.name)
			}
		})
	}
	// And a file that was never written at all.
	if _, ok := LoadSessionState(filepath.Join(dir, "absent.state.json")).Composer(); ok {
		t.Error("a missing sidecar produced a draft")
	}
}

// Over the cap we refuse and say so. Storing a prefix would look like a whole
// draft and the user would find out by reading carefully.
func TestAnOversizeDraftIsRefusedRatherThanTruncated(t *testing.T) {
	path := statePath(t)
	var st SessionState
	if err := st.SetComposer(ComposerDraft{Text: strings.Repeat("x", MaxSessionStateBytes+1)}); err != nil {
		t.Fatal(err)
	}
	err := SaveSessionState(path, st)
	if !errors.Is(err, ErrSessionStateTooLarge) {
		t.Fatalf("SaveSessionState error = %v, want ErrSessionStateTooLarge", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a refused save still wrote a file: the user would get back a draft that is not what they wrote")
	}
}

// The sidecar holds the user's own prose, so it gets config.json's treatment.
func TestTheSidecarIsWrittenPrivately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not apply on windows")
	}
	path := statePath(t)
	var st SessionState
	if err := st.SetComposer(ComposerDraft{Text: "private prose"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionState(path, st); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("sidecar mode = %04o, want 0600 — a draft is user prose and readable by others", got)
	}
}

// An empty sidecar and no sidecar mean the same thing, so clearing the last
// tenant takes the file with it rather than littering the sessions directory.
func TestClearingTheLastTenantRemovesTheFile(t *testing.T) {
	path := statePath(t)
	var st SessionState
	if err := st.SetComposer(ComposerDraft{Text: "temporary"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionState(path, st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: the sidecar was not written: %v", err)
	}

	st.ClearComposer()
	if err := SaveSessionState(path, st); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an empty state left a file behind")
	}
}

// A blank draft is not a draft: restoring one would put nothing in the composer
// and call it a restore.
func TestABlankDraftIsNotStored(t *testing.T) {
	path := statePath(t)
	var st SessionState
	if err := st.SetComposer(ComposerDraft{Text: "   \n  "}); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Composer(); ok {
		t.Fatal("a whitespace-only draft was stored")
	}
	if err := SaveSessionState(path, st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a whitespace-only draft produced a sidecar")
	}
}

// Setting one tenant must not disturb another. Cheap to assert now, and the
// point of the design once a second tenant exists.
func TestSettingOneTenantLeavesTheOthersAlone(t *testing.T) {
	path := statePath(t)
	seeded := `{"version":1,"other_tenant":{"keep":"me"}}`
	if err := os.WriteFile(path, []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	st := LoadSessionState(path)
	if err := st.SetComposer(ComposerDraft{Text: "added", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	st.ClearComposer()
	if err := SaveSessionState(path, st); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file went away, taking a tenant we never touched: %v", err)
	}
	if !strings.Contains(string(b), "other_tenant") {
		t.Errorf("other_tenant was lost: %s", b)
	}
}
