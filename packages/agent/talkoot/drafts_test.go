package talkoot

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// A draft lists only in the workspace that made it and replaces itself. Each
// workspace has its own ids and its own cap, and a refusal names no other
// workspace.
func TestADraftBelongsToOneWorkspace(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	at := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if replaced, err := SaveDraft(Draft{ID: "lake", Home: "/src/a", Text: "one", At: at}); err != nil || replaced {
		t.Fatalf("first save: replaced %v, %v", replaced, err)
	}
	if replaced, err := SaveDraft(Draft{ID: "lake", Home: "/src/a/", Text: "two", At: at}); err != nil || !replaced {
		t.Fatalf("a draft did not replace itself: replaced %v, %v", replaced, err)
	}
	if _, err := SaveDraft(Draft{ID: "lake", Home: "/src/b", Text: "b's", At: at}); err != nil {
		t.Errorf("another workspace could not use the same id: %v", err)
	}
	if got, _ := Drafts("/src/a"); len(got) != 1 || got[0].Text != "two" {
		t.Fatalf("drafts in a: %+v", got)
	}
	if got, _ := Drafts("/src/b"); len(got) != 1 || got[0].Text != "b's" {
		t.Errorf("drafts in b: %+v", got)
	}
	for _, bad := range []string{"../x", "Lake", ""} {
		if _, err := SaveDraft(Draft{ID: bad, Home: "/src/a"}); err == nil {
			t.Errorf("draft id %q saved", bad)
		}
		if _, err := LoadDraft("/src/a", bad); !errors.Is(err, ErrDraftNotFound) {
			t.Errorf("draft id %q loaded: %v", bad, err)
		}
	}
	if _, err := SaveDraft(Draft{ID: "big", Home: "/src/a", Why: strings.Repeat("x", MaxDraftWhyBytes+1)}); err == nil {
		t.Error("an oversized reason saved")
	}
	if ok, err := DeleteDraft("/src/a", "lake"); !ok || err != nil {
		t.Errorf("a could not delete its draft: %v %v", ok, err)
	}
	if _, err := os.Stat(draftPath("/src/a", "lake")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the draft file stays: %v", err)
	}
	if got, _ := Drafts("/src/b"); len(got) != 1 {
		t.Errorf("a's delete reached b: %+v", got)
	}

	for i := range MaxDrafts {
		if _, err := SaveDraft(Draft{ID: "d" + strings.Repeat("a", i+1), Home: "/src/a", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := SaveDraft(Draft{ID: "over", Home: "/src/a"})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("a draft past the cap: %v", err)
	}
	if _, err := SaveDraft(Draft{ID: "da", Home: "/src/a", Text: "again"}); err != nil {
		t.Errorf("a replace at the cap was refused: %v", err)
	}
	if _, err := SaveDraft(Draft{ID: "other", Home: "/src/b"}); err != nil {
		t.Errorf("a's full cap blocked b: %v", err)
	}
}

// Drafts lists newest first. A file that does not parse lists with its
// problem, and a discard still removes it.
func TestADraftThatDoesNotReadCanStillBeDiscarded(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	base := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"old", "mid", "new"} {
		if _, err := SaveDraft(Draft{ID: id, Home: "/src/a", At: base.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Drafts("/src/a")
	if len(got) != 3 || got[0].ID != "new" || got[2].ID != "old" {
		t.Fatalf("order: %+v", got)
	}
	if err := os.WriteFile(draftPath("/src/a", "mid"), []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDraft("/src/a", "mid"); err == nil || errors.Is(err, ErrDraftNotFound) {
		t.Errorf("a torn draft reads as %v, want a parse error", err)
	}
	got, _ = Drafts("/src/a")
	var torn *Draft
	for i := range got {
		if got[i].ID == "mid" {
			torn = &got[i]
		}
	}
	if torn == nil || torn.Problem == "" {
		t.Fatalf("the torn draft is not listed with a problem: %+v", got)
	}
	if ok, err := DeleteDraft("/src/a", "mid"); !ok || err != nil {
		t.Errorf("the torn draft could not be discarded: %v %v", ok, err)
	}
}

// Concurrent saves of new ids never pass the cap.
func TestConcurrentDraftsStopAtTheCap(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	var wg sync.WaitGroup
	for i := range MaxDrafts + 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = SaveDraft(Draft{ID: "c" + strings.Repeat("a", i+1), Home: "/src/a", At: time.Now()})
		}()
	}
	wg.Wait()
	if got, _ := Drafts("/src/a"); len(got) != MaxDrafts {
		t.Errorf("%d drafts after concurrent saves, want %d", len(got), MaxDrafts)
	}
}
