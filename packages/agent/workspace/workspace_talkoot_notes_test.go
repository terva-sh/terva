package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
)

// seatOf wakes a member with a post, so it gets a session and a seat, and
// returns the seat.
func seatOf(t *testing.T, w *Workspace, member string) talkootSeat {
	t.Helper()
	if _, err := w.talkootPost(context.Background(), "crew", "sothr", []string{member}, "Wake up, "+member+".", nil, ""); err != nil {
		t.Fatal(err)
	}
	var sessID string
	waitTalkoot(t, member+"'s session", func() bool {
		sessID = memberView(t, w, "crew", member).Session
		return sessID != ""
	})
	seat, ok := w.talkootSeatOf(sessID)
	if !ok {
		t.Fatalf("%s holds no seat", member)
	}
	return seat
}

// A member writes a note under its own id, and every member reads it.
func TestAMemberWritesANoteThatEveryMemberReads(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	if _, err := w.talkootCreate(context.Background(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(talkoot.Dir(), "crew", talkoot.NotesDir)); err != nil || !fi.IsDir() {
		t.Fatalf("the talkoot has no notes directory: %v", err)
	}
	helm, jev := seatOf(t, w, "helm"), seatOf(t, w, "jev")
	ref, err := helm.WriteNote("plan.md", "the plan")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "note:helm/plan.md" {
		t.Errorf("helm's note is %q: the seat names the author", ref)
	}
	if text, err := jev.ReadNote(ref); err != nil || text != "the plan" {
		t.Errorf("jev reads %q, %v", text, err)
	}
	if notes, err := jev.ListNotes(); err != nil || len(notes) != 1 || notes[0].Ref != ref {
		t.Errorf("jev lists %+v, %v", notes, err)
	}
	// The seat supplies the author, so no name reaches another member's
	// directory.
	if _, err := jev.WriteNote("../helm/plan.md", "overwritten"); err == nil {
		t.Error("jev wrote outside its own notes")
	}
	if text, _ := jev.ReadNote(ref); text != "the plan" {
		t.Errorf("helm's note now reads %q", text)
	}
}

// A seat that lost its place writes and reads no notes.
func TestARevokedSeatReachesNoNotes(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	if _, err := w.talkootCreate(context.Background(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	helm := seatOf(t, w, "helm")
	helm.b.revoke()
	if _, err := helm.WriteNote("plan.md", "x"); err == nil {
		t.Error("a revoked seat wrote a note")
	}
	if _, err := helm.ListNotes(); err == nil {
		t.Error("a revoked seat listed the notes")
	}
}

// A talkoot made before the notes existed gets its directory when it starts.
func TestStartMakesTheNotesDirectoryOfAnOlderTalkoot(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	if _, err := w.talkootCreate(context.Background(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(talkoot.Dir(), "crew", talkoot.NotesDir)
	_ = w.Close()
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	openTalkootWorkspace(t, cwd).LoadTalkoots()
	if fi, err := os.Stat(notes); err != nil || !fi.IsDir() {
		t.Errorf("the restarted talkoot has no notes directory: %v", err)
	}
}
