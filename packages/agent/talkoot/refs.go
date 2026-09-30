package talkoot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// MaxRefBytes caps what a person's view reads of one path: reference. A note
// is capped at MaxNoteBytes when it is written, so this bounds a checkout file.
const MaxRefBytes = 256 * 1024

// RefText is the text a path: or note: reference names, as a person's view
// shows it in place.
type RefText struct {
	Ref  string
	Text string
	// Size is the file's size, which is more than len(Text) when Truncated.
	Size      int64
	Truncated bool
	// Binary is set for a file that is not UTF-8 text. Text is then empty.
	Binary bool
}

// ReadRef reads the file a path: or note: reference names, for a person's
// view. dir is the talkoot's directory, which holds the notes, and home is
// its home checkout. Any other kind of reference has no file to open.
//
// 🚨 A path: is opened through an os.Root on the home checkout, so a symlink
// that leads out of the checkout fails, and a note goes through ReadNote,
// which follows no symlink at all. The router checks each reference's shape
// when a member sends it. A person's view reads a reference later, after
// anyone with the checkout could have replaced the file with a link.
func ReadRef(dir, home, ref string) (RefText, error) {
	kind, value, _ := strings.Cut(ref, ":")
	switch kind {
	case "note":
		text, err := ReadNote(dir, ref)
		if err != nil {
			return RefText{}, err
		}
		return RefText{Ref: ref, Text: text, Size: int64(len(text))}, nil
	case "path":
		return readPath(home, value)
	}
	return RefText{}, fmt.Errorf("talkoot: %q is not a path: or note: reference, so there is no file to open", ref)
}

func readPath(home, value string) (RefText, error) {
	if err := checkPathRef(value, remedy{person: true}); err != nil {
		return RefText{}, err
	}
	ref := "path:" + value
	r, err := os.OpenRoot(home)
	if err != nil {
		return RefText{}, fmt.Errorf("talkoot: open the home checkout: %w", err)
	}
	defer r.Close()
	name := path.Clean(value)
	// 🚨 The mode is checked before the open, and the open does not wait. A
	// named pipe blocks an open for reading until a writer comes, and any
	// member with a shell can make one and cite it.
	before, err := r.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return RefText{}, fmt.Errorf("talkoot: %s does not exist in the home checkout", ref)
	}
	if err != nil {
		return RefText{}, fmt.Errorf("talkoot: %s cannot be opened: %w", ref, err)
	}
	if !before.Mode().IsRegular() {
		return RefText{}, fmt.Errorf("talkoot: %s is not a file", ref)
	}
	f, err := r.OpenFile(name, os.O_RDONLY|openNonblock, 0)
	if err != nil {
		return RefText{}, fmt.Errorf("talkoot: %s cannot be opened: %w", ref, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return RefText{}, fmt.Errorf("talkoot: %s: %w", ref, err)
	}
	if !fi.Mode().IsRegular() || !os.SameFile(before, fi) {
		return RefText{}, fmt.Errorf("talkoot: %s changed while it was opened; try again", ref)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxRefBytes+1))
	if err != nil {
		return RefText{}, fmt.Errorf("talkoot: read %s: %w", ref, err)
	}
	out := RefText{Ref: ref, Size: fi.Size()}
	if len(b) > MaxRefBytes {
		b, out.Truncated = b[:MaxRefBytes], true
		// A cut can split a character, and the rest must still read as text.
		for len(b) > 0 && !utf8.Valid(b) && len(b) > MaxRefBytes-utf8.UTFMax {
			b = b[:len(b)-1]
		}
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		out.Binary = true
		return out, nil
	}
	out.Text = string(b)
	return out, nil
}
