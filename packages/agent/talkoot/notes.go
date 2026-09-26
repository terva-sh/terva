package talkoot

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"

	"terva.sh/terva/packages/privfs"
)

// NotesDir is the directory under a talkoot's directory that holds its notes.
// A member writes a report for the team there, and every member reads it.
// Each member writes only under its own id, so a note's path names its author
// and no member can replace another's note.
//
// 🚨 Every read and write goes through an os.Root on this directory, so no
// symlink, planted or swapped in, can reach a file outside it. room.key sits
// one level up.
//
// 🔑 The notes live in $TERVA_HOME and not in the home checkout. They survive
// a lost checkout, and they stay out of the repository's history. A member's
// sandbox does not reach $TERVA_HOME, so members read and write notes through
// their team tools.
const NotesDir = "notes"

// MaxNoteBytes caps one note. A longer report belongs in the repository.
const MaxNoteBytes = 256 * 1024

// MaxNotesPerMember caps how many notes one member keeps. The write auto-admits
// in most modes, so the cap is what bounds a member's use of the disk. A
// member replaces an old note by writing its name again.
const MaxNotesPerMember = 200

// notePattern is a note's name: no separator, and no leading dot, so the
// name alone cannot leave its member's directory or hide.
var notePattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,63}$`)

// NoteRef is the reference that cites a member's note.
func NoteRef(member, name string) string { return "note:" + member + "/" + name }

// parseNote splits the value of a note: reference into its member and name.
func parseNote(value string, fix remedy) (member, name string, err error) {
	member, name, ok := strings.Cut(value, "/")
	if !ok || !idPattern.MatchString(member) || !notePattern.MatchString(name) {
		return "", "", fmt.Errorf("talkoot: note reference %q must be note:<member>/<name>, where the name is 1 to 64 letters, digits, and . _ - and does not start with a dot; %s", "note:"+value, fix.noteShape())
	}
	return member, name, nil
}

// A remedy names the fix a refusal offers. A member has the team tools. A
// person posts from a client, and has none of them.
type remedy struct{ person bool }

func (r remedy) path() string {
	if r.person {
		return "cite a path relative to the talkoot's home checkout, as in path:src/main.go, or the branch: or commit: that holds the file"
	}
	return "cite a path relative to the talkoot's home checkout, as in path:src/main.go, or write the file with talkoot_note_write and cite the note: reference it returns"
}

func (r remedy) longBody() string {
	if r.person {
		return "put long work in a file in the home checkout and send a path: reference"
	}
	return "put long work in a note with talkoot_note_write and send the note: reference"
}

func (r remedy) noteShape() string {
	if r.person {
		return "cite the note by the reference the room shows"
	}
	return "cite the reference as talkoot_note_write returned it"
}

func (r remedy) missingNote() string {
	if r.person {
		return "cite a note that a member wrote, by the reference the room shows"
	}
	return "write it with talkoot_note_write first, and cite the reference that returns"
}

// checkPathRef refuses a path: reference that cannot resolve under the
// talkoot's home checkout for every reader.
//
// 🚨 A path a member cites must name the same file for each teammate and for
// the person. An absolute path, or a home path such as ~/x, names a file on one
// machine, and a path that climbs out of the checkout names a file no other
// reader has.
func checkPathRef(value string, fix remedy) error {
	switch {
	case strings.HasPrefix(value, "/") || strings.HasPrefix(value, string(backslash)) || hasVolume(value):
		return fmt.Errorf("talkoot: path:%s is absolute, and no other reader has that file; %s", value, fix.path())
	case strings.HasPrefix(value, "~"):
		return fmt.Errorf("talkoot: path:%s names a file in one user's home directory, and no other reader has it; %s", value, fix.path())
	case strings.ContainsRune(value, backslash):
		return fmt.Errorf("talkoot: path:%s holds a backslash; separate its parts with /, and %s", value, fix.path())
	}
	switch c := path.Clean(value); {
	case c == "." || c == ".." || strings.HasPrefix(c, "../"):
		return fmt.Errorf("talkoot: path:%s leaves the talkoot's home checkout; %s", value, fix.path())
	}
	return nil
}

// backslash is one backslash, U+005C, named so that no rendering of a diff
// can show it as two.
const backslash = '\u005c'

// hasVolume reports a drive letter, as in C:, which names a Windows volume.
func hasVolume(value string) bool {
	return len(value) >= 2 && value[1] == ':' &&
		(('a' <= value[0] && value[0] <= 'z') || ('A' <= value[0] && value[0] <= 'Z'))
}

// errNoNotes is openNotes on a talkoot whose notes directory does not exist.
var errNoNotes = errors.New("talkoot: the talkoot has no notes yet")

// openNotes opens a talkoot's notes directory as a root.
//
// 🚨 os.OpenRoot follows a link in the path it opens, so the directory is
// checked first and compared after. Once the root is open it holds the
// directory itself, and a later swap of the path changes nothing.
func openNotes(dir string) (*os.Root, error) {
	p := filepath.Join(dir, NotesDir)
	before, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoNotes
	}
	if err != nil {
		return nil, fmt.Errorf("talkoot: notes: %w", err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("talkoot: %s is not a directory, so the talkoot's notes cannot be read", p)
	}
	r, err := os.OpenRoot(p)
	if err != nil {
		return nil, fmt.Errorf("talkoot: notes: %w", err)
	}
	if after, err := r.Stat("."); err != nil || !os.SameFile(before, after) {
		r.Close()
		return nil, errors.New("talkoot: the notes directory changed while it was opened; try again")
	}
	return r, nil
}

// regularIn reports whether name in r is a regular file, not a link and not a
// directory. A link inside the notes could point at another member's note, so
// none passes for a note.
func regularIn(r *os.Root, name string) (fs.FileInfo, bool) {
	fi, err := r.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	return fi, true
}

// memberDir refuses a member directory that is a link. It reports whether the
// directory exists.
func memberDir(r *os.Root, member string) (bool, error) {
	fi, err := r.Lstat(member)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("talkoot: notes: %w", err)
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("talkoot: notes/%s is not a directory, so its notes cannot be used", member)
	}
	return true, nil
}

// noteExists reports whether a note is a regular file.
func noteExists(dir, value string, fix remedy) error {
	member, name, err := parseNote(value, fix)
	if err != nil {
		return err
	}
	missing := fmt.Errorf("talkoot: note:%s does not exist; %s", value, fix.missingNote())
	r, err := openNotes(dir)
	if errors.Is(err, errNoNotes) {
		return missing
	}
	if err != nil {
		return err
	}
	defer r.Close()
	if ok, err := memberDir(r, member); err != nil || !ok {
		if err != nil {
			return err
		}
		return missing
	}
	if _, ok := regularIn(r, member+"/"+name); !ok {
		return missing
	}
	return nil
}

// Note is one note as a listing shows it.
type Note struct {
	Ref     string
	Member  string
	Name    string
	Size    int64
	ModTime time.Time
}

// WriteNote writes or replaces one of member's notes in the talkoot at dir,
// and returns the reference that cites it.
//
// The note is written to a temporary name and renamed over the old one, so a
// reader never sees half a note. The temporary name starts with a dot, which
// no note can, so a listing never shows it.
func WriteNote(dir, member, name, text string) (string, error) {
	if !idPattern.MatchString(member) {
		return "", fmt.Errorf("talkoot: %q is not a member id", member)
	}
	if !notePattern.MatchString(name) {
		return "", fmt.Errorf("talkoot: note name %q must be 1 to 64 letters, digits, and . _ - and must not start with a dot", name)
	}
	if len(text) > MaxNoteBytes {
		return "", fmt.Errorf("talkoot: the note is %d bytes, above the %d limit; put a longer report in the repository and cite its path", len(text), MaxNoteBytes)
	}
	if err := privfs.MkdirAll(filepath.Join(dir, NotesDir)); err != nil {
		return "", fmt.Errorf("talkoot: notes: %w", err)
	}
	r, err := openNotes(dir)
	if err != nil {
		return "", err
	}
	defer r.Close()
	ok, err := memberDir(r, member)
	if err != nil {
		return "", err
	}
	if !ok {
		if err := r.Mkdir(member, privfs.DirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("talkoot: notes: %w", err)
		}
		if _, err := memberDir(r, member); err != nil {
			return "", err
		}
	}
	target := member + "/" + name
	switch fi, err := r.Lstat(target); {
	case errors.Is(err, fs.ErrNotExist):
		if n := countNotes(r, member); n >= MaxNotesPerMember {
			return "", fmt.Errorf("talkoot: you already keep %d notes, the limit; write over an old note by its name instead", n)
		}
	case err != nil:
		return "", fmt.Errorf("talkoot: write note: %w", err)
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("talkoot: note:%s/%s is a link or a directory, and a note is never written through one", member, name)
	}
	tmp, err := tempName(member)
	if err != nil {
		return "", err
	}
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privfs.FileMode)
	if err != nil {
		return "", fmt.Errorf("talkoot: write note: %w", err)
	}
	_, werr := f.Write([]byte(text))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	// 🔑 A rename replaces a link at the target rather than following it, and
	// the root keeps both names inside the notes.
	if werr == nil {
		werr = r.Rename(tmp, target)
	}
	if werr != nil {
		_ = r.Remove(tmp)
		return "", fmt.Errorf("talkoot: write note: %w", werr)
	}
	return NoteRef(member, name), nil
}

func tempName(member string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("talkoot: write note: %w", err)
	}
	return member + "/.tmp-" + hex.EncodeToString(b[:]), nil
}

// ReadNote returns the text of the note that ref cites, in the talkoot at dir.
func ReadNote(dir, ref string) (string, error) {
	value, ok := strings.CutPrefix(ref, "note:")
	if !ok {
		return "", fmt.Errorf("talkoot: %q is not a note: reference", ref)
	}
	member, name, err := parseNote(value, remedy{})
	if err != nil {
		return "", err
	}
	missing := fmt.Errorf("talkoot: note:%s does not exist", value)
	r, err := openNotes(dir)
	if errors.Is(err, errNoNotes) {
		return "", missing
	}
	if err != nil {
		return "", err
	}
	defer r.Close()
	if ok, err := memberDir(r, member); err != nil || !ok {
		if err != nil {
			return "", err
		}
		return "", missing
	}
	target := member + "/" + name
	before, ok := regularIn(r, target)
	if !ok {
		return "", missing
	}
	f, err := r.Open(target)
	if err != nil {
		return "", fmt.Errorf("talkoot: read note: %w", err)
	}
	defer f.Close()
	// The root keeps the open inside the notes. SameFile keeps it on the file
	// checked, so a link swapped in for the note after the check fails.
	if after, err := f.Stat(); err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return "", fmt.Errorf("talkoot: note:%s changed while it was read; read it again", value)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxNoteBytes+1))
	if err != nil {
		return "", fmt.Errorf("talkoot: read note: %w", err)
	}
	if len(b) > MaxNoteBytes {
		return "", fmt.Errorf("talkoot: note:%s is above the %d byte limit, so it was not written by talkoot_note_write", value, MaxNoteBytes)
	}
	return string(b), nil
}

// countNotes counts the notes in one member's directory.
func countNotes(r *os.Root, member string) int {
	files, err := fs.ReadDir(r.FS(), member)
	if err != nil {
		return 0
	}
	n := 0
	for _, f := range files {
		if f.Type().IsRegular() && notePattern.MatchString(f.Name()) {
			n++
		}
	}
	return n
}

// ListNotes lists every note in the talkoot at dir, newest first. It skips
// anything that is not a note: a link, a directory inside a member's
// directory, or a name a note cannot have.
func ListNotes(dir string) ([]Note, error) {
	r, err := openNotes(dir)
	if errors.Is(err, errNoNotes) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	members, err := fs.ReadDir(r.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("talkoot: list notes: %w", err)
	}
	var out []Note
	for _, m := range members {
		if !m.IsDir() || !idPattern.MatchString(m.Name()) {
			continue
		}
		files, err := fs.ReadDir(r.FS(), m.Name())
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.Type().IsRegular() || !notePattern.MatchString(f.Name()) {
				continue
			}
			fi, err := f.Info()
			if err != nil {
				continue
			}
			out = append(out, Note{Ref: NoteRef(m.Name(), f.Name()), Member: m.Name(), Name: f.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].ModTime.After(out[j].ModTime)
		}
		return out[i].Ref < out[j].Ref
	})
	return out, nil
}

// checkNotes refuses a send that cites a note the talkoot does not hold.
func (rt *Router) checkNotes(refs []string, fix remedy) error {
	for _, ref := range refs {
		if value, ok := strings.CutPrefix(ref, "note:"); ok {
			if err := noteExists(rt.room.dir, value, fix); err != nil {
				return err
			}
		}
	}
	return nil
}
