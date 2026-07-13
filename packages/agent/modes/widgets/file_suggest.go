package widgets

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sahilm/fuzzy"

	"terva.sh/terva/packages/fswalk"
	"terva.sh/terva/packages/tui"
)

// The walk itself — gitignore handling, .git pruning, the entry/depth caps
// that keep per-keystroke fuzzy ranking under a frame — lives in
// packages/fswalk, shared with the daemon's files.list verb so an attached
// picker completes over exactly the entries a local one would.

// remoteTTL bounds how long a wire-fetched listing serves before the next
// popup interaction refetches. The local scans re-read on directory mtime;
// no such signal crosses the wire, so freshness is time-based.
const remoteTTL = 15 * time.Second

// RemoteLister fetches the file listing from elsewhere — the attach entry
// point wires it to the daemon's files.list verb. It runs on a background
// goroutine (never the render path) and returns entries relative to the
// workspace root plus whether the walk was cap-truncated.
type RemoteLister func(dir string, recursive, respectGitignore bool) ([]fswalk.Entry, bool, error)

// FileSuggester provides an @-triggered file/directory picker popup.
// Type "@" followed by an optional filter to list files in the working
// directory. Arrow up/down navigate, enter selects, esc cancels.
// Right arrow opens a directory, left arrow goes back to the parent.
type FileSuggester struct {
	cursor      int
	lastMatches []FileEntry
	cwd         string // project root
	browseRel   string // relative path from cwd we're currently browsing ("" = cwd itself)
	// recursive enables a whole-tree fuzzy search instead of
	// directory-by-directory browsing. Mirrors the persisted
	// recursive_file_suggest setting; toggled live from /settings.
	recursive bool
	// respectGitignore drops entries matched by the project's root
	// .gitignore (and always .git) from both the flat and recursive
	// listings. Mirrors the persisted respect_gitignore setting; on by
	// default. Toggled live from /settings.
	respectGitignore bool

	// remote, when set, replaces local disk reads with wire fetches; the
	// fills land on a background goroutine and onUpdate requests a repaint,
	// so the popup pops in when data arrives instead of a frame blocking on
	// the network. Everything else — fuzzy ranking, navigation, chips — is
	// unchanged.
	remote   RemoteLister
	onUpdate func()

	// mu guards the cache fields below: in remote mode a fill goroutine
	// writes them while the render path reads. Local mode never contends
	// (everything runs on the main goroutine) but locks uniformly — the
	// section is a few loads. The slice is replaced, never mutated, so a
	// returned cache stays valid after release.
	mu        sync.Mutex
	cachedDir string // absolute directory we last scanned (local mode)
	cachedAll []FileEntry
	// cachedMTime is the mtime of cachedDir at the time of the scan.
	// scan() compares the current mtime against this on every call and
	// re-reads the directory if it has changed, so files or folders
	// created mid-session show up in the picker without restarting terva.
	// Stat is cheap (single syscall) so doing it per keystroke while
	// the popup is open does not impact responsiveness.
	cachedMTime time.Time
	// Remote-mode cache slot: which (dir|mode) the entries answer, when
	// they landed (remoteTTL freshness), and whether a fill is in flight.
	remoteKey      string
	remoteAt       time.Time
	remoteFetching bool
}

type FileEntry struct {
	name  string // display name
	Rel   string // path relative to cwd (used for chip insertion)
	IsDir bool
}

// NewFileSuggester returns a picker that respects .gitignore by
// default. Callers override via SetRespectGitignore once the persisted
// setting is known.
func NewFileSuggester() *FileSuggester { return &FileSuggester{respectGitignore: true} }

// SetRemoteLister switches the picker onto a wire-backed listing (nil keeps
// local disk). onUpdate is called — from a background goroutine — when a
// fill lands, so the host can request a repaint (the popup appears when the
// data does).
func (s *FileSuggester) SetRemoteLister(fn RemoteLister, onUpdate func()) {
	s.remote = fn
	s.onUpdate = onUpdate
	s.dropCache()
}

// dropCache invalidates both cache slots (local and remote).
func (s *FileSuggester) dropCache() {
	s.mu.Lock()
	s.cachedDir = ""
	s.cachedAll = nil
	s.remoteKey = ""
	s.remoteAt = time.Time{}
	s.mu.Unlock()
}

// SetCWD updates the project root.
func (s *FileSuggester) SetCWD(cwd string) {
	if s.cwd != cwd {
		s.cwd = cwd
		s.browseRel = ""
		s.dropCache()
	}
}

// SetRecursive toggles whole-tree fuzzy search. Switching modes drops
// the cache and resets the browse position so the next render reflects
// the new mode immediately.
func (s *FileSuggester) SetRecursive(on bool) {
	if s.recursive == on {
		return
	}
	s.recursive = on
	s.browseRel = ""
	s.dropCache()
	s.cursor = 0
}

// SetRespectGitignore toggles .gitignore filtering for both modes.
// Switching drops the cache so the next scan reflects the new state.
func (s *FileSuggester) SetRespectGitignore(on bool) {
	if s.respectGitignore == on {
		return
	}
	s.respectGitignore = on
	s.dropCache()
	s.cursor = 0
}

// browseDir returns the absolute directory currently being browsed.
func (s *FileSuggester) browseDir() string {
	if s.browseRel == "" {
		return s.cwd
	}
	return filepath.Join(s.cwd, s.browseRel)
}

// scan returns entries for the current browse position: remote mode serves
// the wire cache (kicking an async fill when stale), local mode reads disk
// through fswalk.
//
// Local results are cached by absolute path + mtime: a repeated call
// against the same directory returns the cached slice when nothing has
// changed on disk, and re-reads when an entry was added, removed, or
// renamed (any of which bumps the directory's mtime on every filesystem
// terva supports; in recursive mode the root mtime is a best-effort
// signal, with Invalidate() and the toggle drops keeping it fresh). A
// failed stat falls through to a fresh read rather than returning a stale
// cache so transient errors self-heal.
func (s *FileSuggester) scan() []FileEntry {
	if s.remote != nil {
		return s.scanRemote()
	}
	statDir := s.browseDir()
	if s.recursive {
		statDir = s.cwd
	}
	var mtime time.Time
	if info, err := os.Stat(statDir); err == nil {
		mtime = info.ModTime()
	}
	s.mu.Lock()
	if s.cachedDir == statDir && s.cachedAll != nil && !mtime.IsZero() && mtime.Equal(s.cachedMTime) {
		all := s.cachedAll
		s.mu.Unlock()
		return all
	}
	s.mu.Unlock()
	entries, _, err := fswalk.List(s.cwd, fswalk.Options{
		Dir:              s.browseRel,
		Recursive:        s.recursive,
		RespectGitignore: s.respectGitignore,
	})
	if err != nil {
		return nil
	}
	all := fromWalk(entries, s.recursive)
	s.mu.Lock()
	s.cachedAll = all
	s.cachedDir = statDir
	s.cachedMTime = mtime
	s.mu.Unlock()
	return all
}

// fromWalk maps fswalk entries onto picker rows: flat mode displays base
// names (the browsed directory is the context), recursive mode full
// relative paths (the fuzzy haystack). A free function taking recursive
// explicitly — the remote fill goroutine must not read main-thread fields.
func fromWalk(entries []fswalk.Entry, recursive bool) []FileEntry {
	all := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Rel
		if !recursive {
			name = filepath.Base(e.Rel)
		}
		all = append(all, FileEntry{name: name, Rel: e.Rel, IsDir: e.IsDir})
	}
	return all
}

// remoteCacheKey identifies which listing the remote cache slot answers.
func (s *FileSuggester) remoteCacheKey() string {
	key := s.browseRel
	if s.recursive {
		key = "\x00recursive"
	}
	if s.respectGitignore {
		key += "\x00gi"
	}
	return key
}

// scanRemote serves the wire cache and never blocks: a stale or missing
// slot kicks one background fill (the popup pops in when it lands, via
// onUpdate → repaint), and until the fill answers the CURRENT key the
// picker shows nothing rather than another directory's entries.
func (s *FileSuggester) scanRemote() []FileEntry {
	key := s.remoteCacheKey()
	s.mu.Lock()
	hit := s.remoteKey == key && s.cachedAll != nil
	fresh := hit && time.Since(s.remoteAt) < remoteTTL
	var rows []FileEntry
	if hit {
		rows = s.cachedAll
	}
	kick := !fresh && !s.remoteFetching
	if kick {
		s.remoteFetching = true
	}
	s.mu.Unlock()
	if kick {
		go s.fetchRemote(key, s.browseRel, s.recursive, s.respectGitignore)
	}
	return rows
}

// fetchRemote is the one background fill: fetch, store under the key the
// request answered, repaint. A failed fetch stores an empty slot so the
// render path doesn't re-kick at frame rate against a broken daemon — the
// TTL (or an explicit Invalidate) retries.
func (s *FileSuggester) fetchRemote(key, dir string, recursive, respectGitignore bool) {
	entries, _, err := s.remote(dir, recursive, respectGitignore)
	var all []FileEntry
	if err == nil {
		all = fromWalk(entries, recursive)
	} else {
		all = []FileEntry{}
	}
	s.mu.Lock()
	s.remoteFetching = false
	s.cachedAll = all
	s.remoteKey = key
	s.remoteAt = time.Now()
	s.mu.Unlock()
	if s.onUpdate != nil {
		s.onUpdate()
	}
}

// extractAtQuery returns the filter string after "@".
func extractAtQuery(input string) (string, bool) {
	input = strings.TrimRight(input, " ")
	idx := strings.LastIndex(input, "@")
	if idx < 0 {
		return "", false
	}
	if idx > 0 && input[idx-1] != ' ' {
		return "", false
	}
	query := input[idx+1:]
	if strings.ContainsAny(query, " \t\n") {
		return "", false
	}
	return query, true
}

// matches returns file entries matching the current @-query.
//
// An empty query returns every entry in scan order. A non-empty query
// is ranked with sahilm/fuzzy: in recursive mode the pattern is matched
// against each entry's path relative to cwd (so "foobar" can find
// "src/foo/bar.go"); in flat mode it matches the entry's display name.
func (s *FileSuggester) matches(input string) []FileEntry {
	query, ok := extractAtQuery(input)
	if !ok {
		return nil
	}
	all := s.scan()
	if len(all) == 0 {
		return nil
	}
	if query == "" {
		return all
	}
	haystack := make([]string, len(all))
	for i, e := range all {
		if s.recursive {
			haystack[i] = e.Rel
		} else {
			haystack[i] = e.name
		}
	}
	ranked := fuzzy.Find(query, haystack)
	out := make([]FileEntry, 0, len(ranked))
	for _, m := range ranked {
		if m.Index >= 0 && m.Index < len(all) {
			out = append(out, all[m.Index])
		}
	}
	return out
}

// Active reports whether the popup should be visible.
func (s *FileSuggester) Active(input string) bool {
	return len(s.matches(input)) > 0
}

// Reset clears state.
func (s *FileSuggester) Reset() {
	s.cursor = 0
	s.lastMatches = nil
	s.browseRel = ""
	s.dropCache()
}

// Invalidate forces a rescan (or, remote, a refetch).
func (s *FileSuggester) Invalidate() {
	s.dropCache()
}

func (s *FileSuggester) Up() {
	if s.cursor > 0 {
		s.cursor--
	}
}

func (s *FileSuggester) Down() {
	if s.cursor < len(s.lastMatches)-1 {
		s.cursor++
	}
}

// Right opens the selected directory, descending into it.
// Returns true if a directory was entered. Disabled in recursive mode,
// where the whole tree is already flattened into the result list.
func (s *FileSuggester) Right() bool {
	if s.recursive {
		return false
	}
	m := s.lastMatches
	if len(m) == 0 || s.cursor >= len(m) {
		return false
	}
	e := m[s.cursor]
	if !e.IsDir {
		return false
	}
	s.browseRel = e.Rel
	s.dropCache()
	s.cursor = 0
	return true
}

// Left goes back to the parent directory.
// Returns true if we moved up. Disabled in recursive mode.
func (s *FileSuggester) Left() bool {
	if s.recursive {
		return false
	}
	if s.browseRel == "" {
		return false
	}
	parent := filepath.Dir(s.browseRel)
	if parent == "." {
		parent = ""
	}
	s.browseRel = parent
	s.dropCache()
	s.cursor = 0
	return true
}

// TabComplete rewrites input's live @-token by one shell-style Tab press
// over the current listing (AtComplete semantics: segment-wise prefix
// completion to unique or longest-common-prefix; a unique directory gains a
// "/" so the token stays live). ok reports the keystroke was consumed — a
// live token existed — even when nothing extended. Runs over the same
// cached entries the popup shows, so it never blocks on the wire; while a
// remote fill is still in flight the press is a consumed no-op and the next
// one completes.
func (s *FileSuggester) TabComplete(input string) (string, bool) {
	query, ok := extractAtQuery(input)
	if !ok {
		return input, false
	}
	all := s.scan()
	cands := make([]AtCandidate, 0, len(all))
	for _, e := range all {
		p := filepath.ToSlash(e.Rel)
		if !s.recursive {
			// Flat mode browses one directory: complete over basenames in
			// that namespace (the query is a filter, not a path).
			p = e.name
		}
		cands = append(cands, AtCandidate{Path: p, Dir: e.IsDir})
	}
	extended, _ := AtComplete(cands, query)
	if !s.recursive {
		// Flat mode has no in-token descent — the popup's → key descends —
		// so a completed directory stays slash-less and selectable.
		extended = strings.TrimSuffix(extended, "/")
	}
	if extended == query {
		return input, true
	}
	idx := strings.LastIndex(input, "@")
	return input[:idx+1] + extended, true
}

// Selection returns the relative path of the currently highlighted entry.
func (s *FileSuggester) Selection(input string) string {
	m := s.matches(input)
	if len(m) == 0 {
		return ""
	}
	if s.cursor >= len(m) {
		s.cursor = len(m) - 1
	}
	return m[s.cursor].Rel
}

// SelectedEntry returns the currently highlighted entry.
func (s *FileSuggester) SelectedEntry(input string) (FileEntry, bool) {
	m := s.matches(input)
	if len(m) == 0 {
		return FileEntry{}, false
	}
	if s.cursor >= len(m) {
		s.cursor = len(m) - 1
	}
	return m[s.cursor], true
}

// Render returns the popup lines.
func (s *FileSuggester) Render(input string, th tui.Theme, width int) []string {
	m := s.matches(input)
	if len(m) == 0 {
		return nil
	}
	s.lastMatches = m
	if s.cursor >= len(m) {
		s.cursor = len(m) - 1
	}

	const maxVisible = 12
	visible := m
	offset := 0
	if len(m) > maxVisible {
		if s.cursor >= maxVisible {
			offset = s.cursor - maxVisible + 1
		}
		end := offset + maxVisible
		if end > len(m) {
			end = len(m)
			offset = end - maxVisible
		}
		visible = m[offset:end]
	}

	var out []string

	// Show breadcrumb when browsing a subdirectory.
	if s.browseRel != "" {
		crumb := th.FG256(th.Accent, "  "+s.browseRel+"/")
		out = append(out, crumb)
	}

	for i, e := range visible {
		idx := offset + i
		name := e.name
		if e.IsDir {
			name += "/"
		}
		plain := "  " + name
		if idx == s.cursor {
			out = append(out, th.PadHighlight(plain, width))
		} else {
			out = append(out, th.FG256(th.Muted, plain))
		}
	}

	out = append(out, "")
	var hint string
	switch {
	case s.recursive:
		hint = "  \u2191/\u2193 navigate - tab complete - enter select - esc cancel (recursive)"
	case s.browseRel != "":
		hint = "  \u2191/\u2193 navigate - \u2192 open - \u2190 back - tab complete - enter select - esc cancel"
	default:
		hint = "  \u2191/\u2193 navigate - \u2192 open dir - tab complete - enter select - esc cancel"
	}
	out = append(out, th.FG256(th.Muted, hint))
	out = append(out, "")
	return out
}

// fileChipRE matches @-picker chips inserted by modes:
// [file:relative/path] and [dir:relative/path/]. The lower-level
// tui.Editor has its own drag/drop chips ([file:N:basename]) which
// Editor.SubmitValue expands before callers reach this helper; if one
// leaks through, leave it untouched rather than guessing at a path.
var fileChipRE = regexp.MustCompile(`\[(file|dir):([^\]]+)\]`)
var editorFileChipRE = regexp.MustCompile(`^\d+:`)

// ExpandFileChips replaces [file:name] and [dir:name/] chips with
// the full path relative to cwd.
func ExpandFileChips(text, cwd string) string {
	return fileChipRE.ReplaceAllStringFunc(text, func(match string) string {
		groups := fileChipRE.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		name := groups[2]
		if editorFileChipRE.MatchString(name) {
			return match
		}
		// Strip trailing slash from dir names.
		name = strings.TrimRight(name, "/")
		return filepath.Join(cwd, name)
	})
}
