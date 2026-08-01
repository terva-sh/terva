// Package memory is agent-curated durable memory: a small markdown file of
// facts the model saves so a future session does not have to re-learn them.
//
// Two scopes share one type, distinguished only by the file they bind to:
//   - PROJECT memory (NewStore) — facts about one repo, keyed by the project's
//     ProjectKey, at <home>/memory/projects/<key>/memory.md.
//   - USER memory (NewUserStore) — cross-project facts about the person, at
//     <home>/memory/user.md.
//
// An empty bound dir ("") means "no target" (--no-session, no resolvable
// project) and the store stays in memory, writing nothing.
//
// This was a terva extension until 2026-07. It moved into core because memory is
// not an optional integration — it is on the critical path of what every future
// session knows about a repo and about the user — and because living outside the
// tree meant living outside `just ci`, outside i18n, and outside every review:
// a 15% tool failure rate across 39 calls went unnoticed until a session log was
// read by hand. See docs/proposals/memory-in-core.md.
//
// Multi-process safety: the in-memory entries are a cache, never the source of
// truth. Every mutation takes a cross-process advisory lock, RELOADS the file,
// applies the change to that fresh state, and writes it back atomically — so two
// terva instances sharing a project MERGE their edits instead of the last writer
// clobbering the other's whole delta. Reads are lock-free because writes are
// atomic (temp+rename), so a reader never sees a half file.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/privfs"
)

// Bounds keep the injected block small enough to fit the system prompt's static
// budget with headroom once both scopes plus the policy header are combined
// (see render.go), and keep any single entry terse.
const (
	// MaxEntryLen bounds one entry, in runes. Raised from 500, which was
	// measured amputating real memories: 3 of 13 entries in a live corpus sat
	// hard against it and ended mid-word, each losing the procedure it existed
	// to record ("To add a new Anthropic model, add a specu…"). 500 is enough
	// for a fact and not enough for a fact plus how to act on it.
	//
	// Over-length is REFUSED, never truncated — see Add. core.CleanOneLine
	// still truncates, and correctly so for its other callers: a task label is
	// display text where shortening is lossless in the way that matters. A
	// memory entry IS the content.
	MaxEntryLen = 1024
	// MaxEntries caps the count so a runaway curator can't grow unbounded.
	// Deliberately decorative: at MaxEntryLen the byte caps bind at ~16
	// entries, and at typical ~400-byte entries at ~40, so no refusal a user
	// sees will cite this. It is a backstop, not a working limit.
	MaxEntries = 100
	// MaxProjectBytes caps the serialized PROJECT file. Raised from 6144,
	// which refused four writes in a single reviewed session. Calibrated
	// against what comparable harnesses actually carry — Claude Code's own
	// project index for this repo is ~19 KB — so the old cap was about a third
	// of observed practice.
	MaxProjectBytes = 16384
	// MaxUserBytes caps the serialized USER file. Still the smaller scope:
	// user-level facts are few and short (role, preferences, environment).
	MaxUserBytes = 4096

	projectFileName = "memory.md"
	userFileName    = "user.md"

	// ScopeProject / ScopeUser are the wire values of the tool's `scope`.
	ScopeProject = "project"
	ScopeUser    = "user"
)

// Labels name a scope in model-facing output. Exported so the tool and the
// /memory command cannot drift on what to call each store.
const (
	LabelProject = "Project memory"
	LabelUser    = "User memory"
)

// projectFileHeader / userFileHeader head each file. Informational only:
// parsing keys off the "- " bullet prefix and ignores everything else, so the
// header (or any hand-edit that isn't a bullet) is tolerated. The file is meant
// to be hand-editable — that is why this is markdown and not JSON.
const projectFileHeader = `# Project memory
#
# Agent-curated, durable notes about this project. Managed by the model via the
# memory tool and by you via /memory. One fact per bullet; keep them terse.
`

const userFileHeader = `# User memory
#
# Agent-curated, durable cross-project notes about the user (you). Managed by
# the model via the memory tool (scope=user) and by you via /memory. One fact
# per bullet; keep them terse.
`

// Store is one scope's memory list. It is safe for concurrent use within a
// process (s.mu) and across processes (a file lock taken per mutation).
type Store struct {
	mu         sync.Mutex
	name       string // base filename (memory.md / user.md)
	header     string // file header for this scope
	label      string // model-facing scope name
	maxEntries int
	maxBytes   int
	dir        string   // bound dir; "" => in-memory only
	entries    []string // cache of the on-disk list; reloaded on every mutation
}

// NewStore returns an empty, unbound PROJECT store. Bind it with Rebind.
func NewStore() *Store {
	return &Store{name: projectFileName, header: projectFileHeader, label: LabelProject,
		maxEntries: MaxEntries, maxBytes: MaxProjectBytes}
}

// NewUserStore returns an empty, unbound USER store. Bind it to the memory root
// so it is shared across every project.
func NewUserStore() *Store {
	return &Store{name: userFileName, header: userFileHeader, label: LabelUser,
		maxEntries: MaxEntries, maxBytes: MaxUserBytes}
}

// Label is the model-facing name of this scope.
func (s *Store) Label() string { return s.label }

// Budget reports the scope's caps and the SERIALIZED size they are measured
// against — not the sum of entry lengths, which understates it by the header and
// the bullets. It is the same number Add refuses on, so a pane showing "15.9 of
// 16 KiB" and a refusal saying "would exceed its 16384-byte budget" cannot
// disagree about how close the scope is to full.
func (s *Store) Budget() (bytes, maxBytes, maxCount int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(serialize(s.header, s.entries)), s.maxBytes, s.maxEntries
}

// Bound reports whether this store has a directory to persist to. False means
// in-memory only (--no-session, no resolvable project): entries are accepted and
// listed for the session and then lost, which a surface must be able to say
// rather than showing a list that silently will not survive.
func (s *Store) Bound() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir != ""
}

// Rebind points the store at a directory and loads its file (an absent file is
// not an error — the scope simply has no memory yet). An empty dir unbinds the
// store to in-memory mode. Binding to the already-bound dir is a no-op, so
// repeated session events are cheap and don't clobber an in-session write; use
// Reload to force a re-read of the same dir.
func (s *Store) Rebind(dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir == s.dir {
		return nil
	}
	s.dir = dir
	s.entries = nil
	if dir == "" {
		return nil
	}
	entries, err := s.load()
	if err != nil {
		return err
	}
	s.entries = entries
	return nil
}

// Reload re-reads the bound file, picking up writes another terva instance (or
// a hand-edit) made since this store last loaded. Unlike Rebind it does NOT skip
// when the dir is unchanged — that is the point. A no-op in in-memory mode.
// Lock-free: writes are atomic, so a concurrent writer is never seen half-applied.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir == "" {
		return nil
	}
	entries, err := s.load()
	if err != nil {
		return err
	}
	s.entries = entries
	return nil
}

// List returns a copy of the current entries in order.
func (s *Store) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.entries...)
}

// Add appends text as a new entry. The text is sanitized to one safe line and
// bounded; the add is rejected if it duplicates an existing entry or would push
// the file past the scope's byte or count cap, evaluated against the freshly
// reloaded on-disk state so a concurrent instance's entries count too.
func (s *Store) Add(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := cleanEntry(text)
	if err != nil {
		return err
	}
	return s.mutateLocked(func(cur []string) ([]string, error) {
		if m, exact := findSimilar(cur, t); m != "" {
			if exact {
				return nil, fmt.Errorf("already saved: %q", m)
			}
			return nil, fmt.Errorf("very similar to an existing entry: %q — use replace to update it, or rephrase if it's a distinct fact", m)
		}
		if len(cur) >= s.maxEntries {
			// The caps are the one class of refusal the model cannot act on
			// without knowing what is already stored, so the listing rides the
			// error. Withholding it cost a turn per refusal in a reviewed
			// session — four of them — even though the success path returns
			// exactly this listing.
			return nil, s.fullError(cur, fmt.Sprintf("memory is full (%d entries max)", s.maxEntries))
		}
		next := append(append([]string(nil), cur...), t)
		if n := len(serialize(s.header, next)); n > s.maxBytes {
			return nil, s.fullError(cur, fmt.Sprintf("memory would exceed its %d-byte budget (%d)", s.maxBytes, n))
		}
		return next, nil
	})
}

// Replace swaps the single entry containing match for text. It errors if no
// entry contains match, or if more than one does (ambiguous — pass a more
// specific substring). Resolved against the freshly reloaded on-disk state.
func (s *Store) Replace(match, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := cleanEntry(text)
	if err != nil {
		return err
	}
	return s.mutateLocked(func(cur []string) ([]string, error) {
		idx, err := s.find(cur, match)
		if err != nil {
			return nil, err
		}
		next := append([]string(nil), cur...)
		next[idx] = t
		if n := len(serialize(s.header, next)); n > s.maxBytes {
			return nil, s.fullError(cur, fmt.Sprintf("memory would exceed its %d-byte budget (%d)", s.maxBytes, n))
		}
		return next, nil
	})
}

// Get returns the single entry containing match, using the same matching rules
// as Replace and Remove — so an entry the model can remove is an entry it can
// name here, with the same errors when the match misses or is ambiguous.
//
// The read half of find. Archiving needs it: moving an active entry into the
// keyed tier has to READ the entry before it can write it anywhere, and doing
// that by scanning List() in the caller would be a second, drifting copy of the
// matching rules.
func (s *Store) Get(match string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, err := s.find(s.entries, match)
	if err != nil {
		return "", err
	}
	return s.entries[idx], nil
}

// Remove deletes the single entry containing match (same matching rules as
// Replace), resolved against the freshly reloaded on-disk state.
func (s *Store) Remove(match string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(func(cur []string) ([]string, error) {
		idx, err := s.find(cur, match)
		if err != nil {
			return nil, err
		}
		next := append([]string(nil), cur...)
		return append(next[:idx], next[idx+1:]...), nil
	})
}

// Clear removes all entries.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mutateLocked(func(cur []string) ([]string, error) { return nil, nil })
}

// fullError renders a cap refusal with the current entries and their sizes, so
// the model can choose what to drop instead of spending a turn asking what is
// there. Sizes are included because the budget is in bytes: "remove a stale
// entry" is unactionable advice when every entry looks the same length.
func (s *Store) fullError(cur []string, reason string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s; remove or shorten an entry first. %s currently holds:", reason, s.label)
	for i, e := range cur {
		fmt.Fprintf(&b, "\n%2d. (%dB) %s", i+1, len(e), e)
	}
	return fmt.Errorf("%s", b.String())
}

// find returns the index of the unique entry containing match, compared as a
// plain (case-sensitive) substring. A miss lists the candidates: the model
// reached for an entry it believed was there, and the useful reply is what IS
// there — not a restatement that its guess was wrong.
func (s *Store) find(entries []string, match string) (int, error) {
	m := strings.TrimSpace(match)
	if m == "" {
		return -1, fmt.Errorf("match is required (a unique substring of the target entry)")
	}
	found, count := -1, 0
	for i, e := range entries {
		if strings.Contains(e, m) {
			found = i
			count++
		}
	}
	switch {
	case count == 0:
		if len(entries) == 0 {
			return -1, fmt.Errorf("no entry contains %q — %s is empty", m, s.label)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "no entry contains %q. %s currently holds:", m, s.label)
		for i, e := range entries {
			fmt.Fprintf(&b, "\n%2d. %s", i+1, e)
		}
		return -1, fmt.Errorf("%s", b.String())
	case count > 1:
		return -1, fmt.Errorf("%q matches %d entries; use a more specific substring", m, count)
	}
	return found, nil
}

// mutateLocked applies fn to the CURRENT on-disk entries and persists the
// result. The caller must hold s.mu. For a bound store it takes a cross-process
// lock, reloads the file (so concurrent instances merge instead of clobber —
// fn's caps and lookups run against the true on-disk state), writes atomically,
// then refreshes the cache. In-memory mode skips the lock and disk. On any fn
// error nothing is written.
func (s *Store) mutateLocked(fn func(cur []string) ([]string, error)) error {
	if s.dir == "" {
		next, err := fn(s.entries)
		if err != nil {
			return err
		}
		s.entries = next
		return nil
	}
	// The lockfile's directory must exist before the lock can be taken, and a
	// first write to a never-used project is exactly the case where it doesn't.
	if err := privfs.MkdirAll(s.dir); err != nil {
		return err
	}
	lock, err := filelock.Acquire(s.lockPath())
	if err != nil {
		return fmt.Errorf("memory lock: %w", err)
	}
	defer lock.Release()
	cur, err := s.load()
	if err != nil {
		return err
	}
	next, err := fn(cur)
	if err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.entries = next
	return nil
}

func (s *Store) path() string     { return filepath.Join(s.dir, s.name) }
func (s *Store) lockPath() string { return s.path() + ".lock" }

// load reads and parses the bound file. A missing file is not an error (the
// scope simply has no memory yet) and yields a nil slice.
func (s *Store) load() ([]string, error) {
	b, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parse(string(b)), nil
}

// save atomically writes entries to the bound file. privfs pins owner-only
// modes: memory holds facts about the user and their repos and lives under
// $TERVA_HOME, which is owner-only by policy — the extension's 0644 predated
// that and is not carried over.
func (s *Store) save(entries []string) error {
	if err := privfs.MkdirAll(s.dir); err != nil {
		return err
	}
	return privfs.WriteFile(s.path(), []byte(serialize(s.header, entries)))
}

// cleanEntry sanitizes text into one storable line and REFUSES anything longer
// than MaxEntryLen instead of shortening it.
//
// Truncation is the wrong failure for a memory. The store's other cap — bytes —
// already refuses, and returns the current listing with it, because a cap the
// model cannot act on without knowing what is stored costs a turn (finding F1).
// Length did the opposite: it silently kept the first MaxEntryLen runes, added
// "…", and reported success. In a real corpus that stored 3 of 13 entries
// mid-word with the actionable half missing — and because RenderForTool echoes
// what was stored, the model SAW the truncation in its own tool result and left
// it there all three times. Surfacing it is not enough; it has to refuse.
//
// max=0 so core.CleanOneLine strips control characters and flattens newlines
// without applying its own truncation. That helper keeps truncating for its
// other callers, correctly: a shortened task label is still a usable label.
func cleanEntry(text string) (string, error) {
	t := core.CleanOneLine(text, 0)
	if t == "" {
		return "", fmt.Errorf("entry text is required")
	}
	if n := utf8.RuneCountInString(t); n > MaxEntryLen {
		return "", fmt.Errorf("entry is %d characters, over the %d limit — shorten it and try again "+
			"(nothing was saved; the alternative would have been storing it cut off mid-sentence)", n, MaxEntryLen)
	}
	return t, nil
}

// serialize renders entries to the on-disk form for the given file header.
func serialize(header string, entries []string) string {
	var b strings.Builder
	b.WriteString(header)
	if len(entries) > 0 {
		b.WriteString("\n")
		for _, e := range entries {
			b.WriteString("- ")
			b.WriteString(e)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// parse extracts entries from a memory file: every line beginning with "- " is
// one entry (sanitized again defensively); all other lines are ignored, which is
// what makes the file safe to hand-edit.
//
// Sanitizes with max=0 — deliberately NOT MaxEntryLen. Applying the cap here
// truncated on every LOAD, so opening memory.md in an editor and writing a
// longer fact destroyed it silently at the next session start. This file
// format is markdown precisely so it can be hand-edited (see the header
// constants); a format that advertises that and then discards the edit is
// worse than one that never invited it. The cap belongs on the write path,
// where a human is present to be told (see cleanEntry).
//
// An over-length line already on disk is therefore kept whole. It counts fully
// against the scope's byte cap, so it cannot hide from the budget.
func parse(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if rest, ok := strings.CutPrefix(line, "- "); ok {
			if e := core.CleanOneLine(rest, 0); e != "" {
				out = append(out, e)
			}
		}
	}
	return out
}
