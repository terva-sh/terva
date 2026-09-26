package tools

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/testsupport"
)

// A member writes a note, and any member reads it back and finds it in the
// list, under the reference the write returned.
func TestANoteRoundTrips(t *testing.T) {
	dir := testsupport.TempDir(t)
	atlas := &fakeSeat{dir: dir, member: "atlas"}
	helm := &fakeSeat{dir: dir, member: "helm"}
	got, err := talkootText(t, &TalkootNoteWriteTool{Seat: atlas}, `{"name":"plan.md","text":"one\ntwo\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "note:atlas/plan.md") {
		t.Errorf("the write does not name its reference: %q", got)
	}
	got, err = talkootText(t, &TalkootNoteReadTool{Seat: helm}, `{"note":"note:atlas/plan.md"}`)
	if err != nil || got != "one\ntwo\n" {
		t.Errorf("helm reads %q, %v", got, err)
	}
	got, err = talkootText(t, &TalkootNoteReadTool{Seat: helm}, `{}`)
	if err != nil || !strings.Contains(got, "note:atlas/plan.md: 8 bytes") {
		t.Errorf("the list reads %q, %v", got, err)
	}
}

// 🚨 A note's name is one path part. It cannot climb out of the member's
// directory, hide, or name another member's directory.
func TestANoteNameIsOnePart(t *testing.T) {
	seat := &fakeSeat{dir: testsupport.TempDir(t), member: "atlas"}
	for _, name := range []string{"", "../x", "helm/plan.md", ".hidden", "..", strings.Repeat("a", 65), "a b"} {
		if _, err := talkootText(t, &TalkootNoteWriteTool{Seat: seat}, fmt.Sprintf(`{"name":%q,"text":"x"}`, name)); err == nil {
			t.Errorf("the note name %q was accepted", name)
		}
	}
	big := strings.Repeat("x", talkoot.MaxNoteBytes+1)
	if _, err := talkootText(t, &TalkootNoteWriteTool{Seat: seat}, fmt.Sprintf(`{"name":"big","text":%q}`, big)); err == nil {
		t.Error("a note above the cap was written")
	}
}

// A long note pages by line, and a cut result names the offset to go on from.
func TestANoteReadPages(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 2500; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	seat := &fakeSeat{dir: testsupport.TempDir(t), member: "atlas"}
	if _, err := seat.WriteNote("long", b.String()); err != nil {
		t.Fatal(err)
	}
	got, _ := talkootText(t, &TalkootNoteReadTool{Seat: seat}, `{"note":"note:atlas/long"}`)
	if !strings.HasSuffix(got, "[note:atlas/long continues: read it again with offset 2001]\n") {
		t.Errorf("the first page ends %q", got[len(got)-80:])
	}
	got, _ = talkootText(t, &TalkootNoteReadTool{Seat: seat}, `{"note":"note:atlas/long","offset":2499,"limit":5}`)
	if got != "line 2499\nline 2500\n" {
		t.Errorf("the last page reads %q", got)
	}
}

// One line above the byte cap still pages past itself.
func TestALineAboveTheCapDoesNotStopThePaging(t *testing.T) {
	seat := &fakeSeat{dir: testsupport.TempDir(t), member: "atlas"}
	wide := strings.Repeat("x", 60*1024)
	if _, err := seat.WriteNote("wide", wide+"\n"+wide+"\nafter\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := talkootText(t, &TalkootNoteReadTool{Seat: seat}, `{"note":"note:atlas/wide"}`)
	if !strings.Contains(got, "[line cut]") || !strings.HasSuffix(got, "read it again with offset 2]\n") {
		t.Errorf("the first page does not page on past the wide line: %q", got[len(got)-80:])
	}
	got, _ = talkootText(t, &TalkootNoteReadTool{Seat: seat}, `{"note":"note:atlas/wide","offset":2}`)
	if !strings.HasSuffix(got, " [line cut]\nafter\n") {
		t.Errorf("the second page ends %q", got[len(got)-40:])
	}
}
