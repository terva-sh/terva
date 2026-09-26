package talkoot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// 🚨 A path: reference must name the same file for every reader. The router
// refuses one that names a file on one machine, or one outside the home
// checkout, and says where to put the file.
func TestAPathReferenceMustResolveUnderTheHomeCheckout(t *testing.T) {
	for _, p := range []string{
		"/workspace/report.md", `\\server\share\x`, `\x`, "C:/x", "c:x",
		"../x", "a/../../x", "..", ".", "./", `src\main.go`,
		"~", "~/report.md", "~bob/x",
	} {
		err := validateRef("path:"+p, remedy{})
		if err == nil {
			t.Errorf("path:%s was accepted", p)
			continue
		}
		if !strings.Contains(err.Error(), "talkoot_note_write") {
			t.Errorf("path:%s: the refusal does not say where to put the file: %v", p, err)
		}
	}
	for _, p := range []string{"src/main.go", "docs/a b.md", "a/../b", "./README.md", "src/main.go:12"} {
		if err := validateRef("path:"+p, remedy{}); err != nil {
			t.Errorf("path:%s was refused: %v", p, err)
		}
	}
}

func TestANoteReferenceNamesAMemberAndAName(t *testing.T) {
	for _, v := range []string{"atlas", "atlas/", "/plan.md", "Atlas/plan.md", "atlas/.x", "atlas/a/b", "atlas/../x", "atlas/" + strings.Repeat("a", 65)} {
		if err := validateRef("note:"+v, remedy{}); err == nil {
			t.Errorf("note:%s was accepted", v)
		}
	}
	if err := validateRef("note:atlas/plan.md", remedy{}); err != nil {
		t.Errorf("note:atlas/plan.md was refused: %v", err)
	}
}

// A send that cites a note the talkoot does not hold is refused when it is
// sent, not when a reader opens it.
func TestASendCitingAMissingNoteIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	if _, err := f.send("helm", Outgoing{To: []string{"atlas"}, Body: "See it.", Refs: []string{"note:helm/plan.md"}}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("a send citing a missing note: %v", err)
	}
	if _, err := f.router.Post("sothr", nil, "See it.", []string{"note:helm/plan.md"}, ""); err == nil {
		t.Error("a post citing a missing note was accepted")
	}
	if _, err := WriteNote(f.dir, "helm", "plan.md", "the plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.send("helm", Outgoing{To: []string{"atlas"}, Body: "See it.", Refs: []string{"note:helm/plan.md"}}); err != nil {
		t.Errorf("a send citing a written note: %v", err)
	}
}

// 🚨 A symlink in the notes never passes for a note. A link in place of a
// note, of a member's directory, or of the notes directory would point a
// reader or a write outside the notes.
func TestANoteIsNeverASymlink(t *testing.T) {
	outside := testsupport.TempDir(t)
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("the note", func(t *testing.T) {
		dir := testsupport.TempDir(t)
		if err := os.MkdirAll(filepath.Join(dir, NotesDir, "atlas"), 0o700); err != nil {
			t.Fatal(err)
		}
		link(t, secret, filepath.Join(dir, NotesDir, "atlas", "plan.md"))
		if _, err := ReadNote(dir, "note:atlas/plan.md"); err == nil {
			t.Error("a linked note was read")
		}
		if _, err := WriteNote(dir, "atlas", "plan.md", "x"); err == nil {
			t.Error("a note was written through a link")
		}
		if b, _ := os.ReadFile(secret); string(b) != "secret" {
			t.Errorf("the file behind the link now reads %q", b)
		}
	})
	t.Run("the member's directory", func(t *testing.T) {
		dir := testsupport.TempDir(t)
		if err := os.MkdirAll(filepath.Join(dir, NotesDir), 0o700); err != nil {
			t.Fatal(err)
		}
		link(t, outside, filepath.Join(dir, NotesDir, "atlas"))
		if _, err := ReadNote(dir, "note:atlas/secret"); err == nil {
			t.Error("a note was read through a linked member directory")
		}
		if _, err := WriteNote(dir, "atlas", "new", "x"); err == nil {
			t.Error("a note was written through a linked member directory")
		}
	})
	t.Run("the notes directory", func(t *testing.T) {
		dir := testsupport.TempDir(t)
		if err := os.MkdirAll(filepath.Join(outside, "atlas"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "atlas", "x"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		link(t, outside, filepath.Join(dir, NotesDir))
		if _, err := ReadNote(dir, "note:atlas/x"); err == nil {
			t.Error("a note was read through a linked notes directory")
		}
		if _, err := ListNotes(dir); err == nil {
			t.Error("a linked notes directory was listed")
		}
	})
}

func link(t *testing.T, target, at string) {
	t.Helper()
	if err := os.Symlink(target, at); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
}

// The list shows each member's notes, newest first, and skips what is not a
// note.
func TestListNotesSkipsWhatIsNotANote(t *testing.T) {
	dir := testsupport.TempDir(t)
	for _, n := range []struct{ member, name string }{{"atlas", "a"}, {"helm", "b"}} {
		if _, err := WriteNote(dir, n.member, n.name, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, NotesDir, "atlas", ".hidden"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, NotesDir, "atlas", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	notes, err := ListNotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, n := range notes {
		refs = append(refs, n.Ref)
	}
	if len(refs) != 2 {
		t.Errorf("the list holds %v, want the two notes", refs)
	}
}

// A member keeps at most MaxNotesPerMember notes. Writing over one it has
// still passes, and another member has its own count.
func TestAMemberKeepsABoundedNumberOfNotes(t *testing.T) {
	dir := testsupport.TempDir(t)
	for i := range MaxNotesPerMember {
		if _, err := WriteNote(dir, "atlas", fmt.Sprintf("n%d", i), "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteNote(dir, "atlas", "one-more", "x"); err == nil || !strings.Contains(err.Error(), "write over an old note") {
		t.Errorf("the note past the limit: %v", err)
	}
	if _, err := WriteNote(dir, "atlas", "n0", "replaced"); err != nil {
		t.Errorf("writing over a kept note: %v", err)
	}
	if _, err := WriteNote(dir, "helm", "n0", "x"); err != nil {
		t.Errorf("another member's first note: %v", err)
	}
}

// A note larger than a write allows, placed by hand, is refused rather than
// read into memory whole.
func TestAnOversizedNoteIsNotRead(t *testing.T) {
	dir := testsupport.TempDir(t)
	if _, err := WriteNote(dir, "atlas", "big", "x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, NotesDir, "atlas", "big"), make([]byte, MaxNoteBytes+10), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadNote(dir, "note:atlas/big"); err == nil {
		t.Error("an oversized note was read")
	}
}

// A person posts from a client, with no team tools, so a refusal of a post
// names a fix a person can make.
func TestARefusedPostNamesAFixAPersonCanMake(t *testing.T) {
	f := newFixture(t, nil)
	for _, refs := range [][]string{{"path:/etc/passwd"}, {"note:helm/plan.md"}, {"note:Helm/plan.md"}, {"note:helm"}} {
		_, err := f.router.Post("sothr", nil, "Read it.", refs, "")
		if err == nil {
			t.Fatalf("a post citing %v was accepted", refs)
		}
		if strings.Contains(err.Error(), "talkoot_note_write") {
			t.Errorf("the refusal of %v names a member's tool: %v", refs, err)
		}
	}
	_, err := f.router.Post("sothr", nil, strings.Repeat("x", MaxBodyBytes+1), nil, "")
	if err == nil || strings.Contains(err.Error(), "talkoot_note_write") {
		t.Errorf("the refusal of a long post: %v", err)
	}
	f.post()
	for _, ref := range []string{"path:/etc/passwd", "note:helm"} {
		if _, err := f.send("helm", Outgoing{To: []string{"atlas"}, Body: "x", Refs: []string{ref}}); err == nil || !strings.Contains(err.Error(), "talkoot_note_write") {
			t.Errorf("a member's refusal of %s must name its note tool: %v", ref, err)
		}
	}
}
