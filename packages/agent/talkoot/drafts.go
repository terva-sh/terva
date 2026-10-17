package talkoot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/privfs"
)

// A draft is a new team that the team manager proposes and a person has not
// created yet (docs/proposals/talkoot-team-manager.md, slice M4). It holds the
// roster as composed, without a home, so the builder previews it as it
// previews its own text. Only the person's Create writes a roster.
//
// 🔑 Drafts live beside the template tier, not under Dir. A directory under
// Dir claims a team id, so a drafts directory there would take one.
//
// 🔑 Each workspace keeps its drafts in its own directory, named by a hash of
// the workspace path. So ids and the cap belong to one workspace: a second
// workspace cannot take an id, fill the cap, or learn the first one's path.

// DraftsDirName is the drafts directory under $TERVA_HOME.
const DraftsDirName = "talkoot-drafts"

// MaxDrafts bounds the drafts that wait at once in one workspace.
const MaxDrafts = 20

// MaxDraftWhyBytes bounds a draft's reason.
const MaxDraftWhyBytes = 2048

// ErrDraftNotFound is returned for a draft id with no draft in this
// workspace.
var ErrDraftNotFound = errors.New("talkoot: no draft has that id here")

// Draft is one team the manager drafted.
type Draft struct {
	ID string `json:"id"`
	// Home is the workspace directory the draft was made in. The team's home
	// is filled in from it at create, so a draft lists only there.
	Home string `json:"home"`
	// Text is the roster as the manager composed it, with no home.
	Text string    `json:"text"`
	Why  string    `json:"why,omitempty"`
	At   time.Time `json:"at"`
	// Session is the manager session that wrote the draft.
	Session string `json:"session,omitempty"`
	// Problem says why a draft file cannot be read. Drafts sets it, and the
	// file never holds it. A draft with a problem can only be discarded.
	Problem string `json:"-"`
}

// DraftsDir is $TERVA_HOME/talkoot-drafts.
func DraftsDir() string { return filepath.Join(config.TervaHome(), DraftsDirName) }

// homeDir is the drafts directory of the workspace at home.
func homeDir(home string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(home)))
	return filepath.Join(DraftsDir(), hex.EncodeToString(sum[:8]))
}

func draftPath(home, id string) string { return filepath.Join(homeDir(home), id+".json") }

// lockDrafts serializes every change to the store, across the processes that
// share this home. A read needs no lock, because each write is an atomic
// rename.
func lockDrafts() (*filelock.Lock, error) {
	return filelock.Acquire(filepath.Join(DraftsDir(), ".lock"))
}

// SaveDraft writes d, and reports whether it replaced a draft of the same id.
// A new draft past MaxDrafts is refused.
func SaveDraft(d Draft) (bool, error) {
	if !ValidID(d.ID) {
		return false, fmt.Errorf("talkoot: draft id %q must be lower case letters, digits, and dashes, starting with a letter", d.ID)
	}
	if len(d.Why) > MaxDraftWhyBytes {
		return false, fmt.Errorf("talkoot: the reason is %d bytes, above the %d limit", len(d.Why), MaxDraftWhyBytes)
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return false, fmt.Errorf("talkoot: %w", err)
	}
	lk, err := lockDrafts()
	if err != nil {
		return false, fmt.Errorf("talkoot: lock the drafts: %w", err)
	}
	defer lk.Release()
	ids, err := draftIDs(d.Home)
	if err != nil {
		return false, err
	}
	replaces := false
	for _, id := range ids {
		if id == d.ID {
			replaces = true
		}
	}
	if !replaces && len(ids) >= MaxDrafts {
		return false, fmt.Errorf("talkoot: %d drafts already wait, the limit; the person creates or discards one first", len(ids))
	}
	if err := privfs.MkdirAll(homeDir(d.Home)); err != nil {
		return false, err
	}
	if err := privfs.WriteFile(draftPath(d.Home, d.ID), raw); err != nil {
		return false, err
	}
	return replaces, nil
}

// Drafts returns the drafts made in the workspace at home, newest first. A
// file that cannot be read lists with its Problem, so a person can discard it.
func Drafts(home string) ([]Draft, error) {
	ids, err := draftIDs(home)
	if err != nil {
		return nil, err
	}
	out := make([]Draft, 0, len(ids))
	for _, id := range ids {
		d, err := LoadDraft(home, id)
		if errors.Is(err, ErrDraftNotFound) {
			continue // removed since the directory read
		}
		if err != nil {
			d = Draft{ID: id, Home: home, Problem: err.Error()}
			if fi, serr := os.Stat(draftPath(home, id)); serr == nil {
				d.At = fi.ModTime()
			}
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// LoadDraft returns the draft id made in the workspace at home. A missing
// draft is ErrDraftNotFound. A file that cannot be read or parsed is its own
// error.
func LoadDraft(home, id string) (Draft, error) {
	if !ValidID(id) {
		return Draft{}, ErrDraftNotFound
	}
	raw, err := os.ReadFile(draftPath(home, id))
	if errors.Is(err, os.ErrNotExist) {
		return Draft{}, ErrDraftNotFound
	}
	if err != nil {
		return Draft{}, fmt.Errorf("talkoot: read draft %s: %w", id, err)
	}
	var d Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return Draft{}, fmt.Errorf("talkoot: draft %s is not valid JSON: %w", id, err)
	}
	if d.ID != id || !sameHome(d.Home, home) {
		return Draft{}, fmt.Errorf("talkoot: draft %s names another draft or workspace", id)
	}
	return d, nil
}

// DeleteDraft removes the draft id made in the workspace at home, whether or
// not its file reads. It reports whether a draft was removed.
func DeleteDraft(home, id string) (bool, error) {
	if !ValidID(id) {
		return false, nil
	}
	lk, err := lockDrafts()
	if err != nil {
		return false, fmt.Errorf("talkoot: lock the drafts: %w", err)
	}
	defer lk.Release()
	if err := os.Remove(draftPath(home, id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("talkoot: remove draft %s: %w", id, err)
	}
	return true, nil
}

// draftIDs lists the draft ids in the workspace at home, readable or not.
func draftIDs(home string) ([]string, error) {
	entries, err := os.ReadDir(homeDir(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	var out []string
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if ok && e.Type().IsRegular() && ValidID(id) {
			out = append(out, id)
		}
	}
	return out, nil
}

func sameHome(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}
