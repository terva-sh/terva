package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/privfs"
)

// The archived tier. An archived memory is not in the cached system prefix; it
// is retrieved by keyword into the per-turn tail when the conversation touches
// it. See docs/proposals/memory-archive-retrieval.md.
//
// The whole design is one line from lore/render.go: "Constant entries are placed
// in the cached system-prompt prefix; triggered entries are scanned and injected
// into the per-turn tail." Archiving is that flag flipping, plus the retrieval
// spec that is the price of not being constant — it is not a file move, not a
// compression step, and not a one-way door.
//
// Which is why the caps here are so much larger than the active tier's.
// Terseness was only ever a proxy for PREFIX cost, and an archived entry has
// none: it costs bytes only on the turns it actually fires, and a fired entry is
// ~100 tokens against the ~$1.80 a prefix invalidation costs on a long
// transcript. Four orders of magnitude. What is rationed is being always-on.
const (
	// MaxArchiveEntryBytes bounds one archived entry's body. Generous next to
	// MaxEntryLen (1024 runes, one line) because archived entries may be
	// multi-line and carry the procedure, not just the topic sentence — that was
	// the whole finding behind the D6 cap raise.
	//
	// Not unlimited, though: a memory that grows to a page is a document, and
	// docs/ already exists for those. The number is a shape constraint, not a
	// budget.
	MaxArchiveEntryBytes = 8192

	// MaxArchiveBytes bounds a scope's whole archive on disk.
	//
	// This one is NOT arbitrary and NOT about context cost — it is the session
	// START cost. Keyword matching needs every candidate resident, so unlike the
	// swarm archive (which is never walked, and whose measured win was exactly
	// that) this archive is read and parsed in full at every session start. The
	// proposal's scale table puts ~1,000 entries x 2 KB ≈ 2 MB at "fine" and
	// ~10,000 at "not fine", so the cap is that row.
	//
	// When this binds, the fix is not a bigger number: it is the idea layer,
	// which lets a small persisted index be matched instead of every body.
	MaxArchiveBytes = 2 << 20 // 2 MiB

	// archiveDirName is the per-scope archive directory, beside that scope's
	// active file.
	archiveDirName = "archive"

	// archiveFileExt is the entry file suffix. Markdown for the same reason the
	// active tier is: these are meant to be readable and hand-editable.
	archiveFileExt = ".md"

	// maxIDLen bounds a minted filename stem. Long enough to stay recognisable
	// as the entry it names, short enough to type in a `recall` call.
	maxIDLen = 48
)

// ArchiveEntry is one archived memory: a body plus the retrieval spec.
//
// The spec is the whole point of the tier, and the reason archiving ADDS
// information rather than removing it. A constant memory needs no spec because
// it is unconditionally present; the spec is precisely the price of not being.
type ArchiveEntry struct {
	// ID is the filename stem, and the handle the model names the entry by in
	// recall / promote / forget. Stable across edits to the body.
	ID string
	// Name is the human-facing title. Empty is allowed; ID stands in.
	Name string
	// Keys are the primary triggers: what someone TYPES when they need this.
	// Note the direction — the vocabulary that matters is the question's, not
	// the answer's. An entry keyed on the identifiers in its own body is the
	// measured failure mode of this design, not a safe default.
	Keys []string
	// SecondaryKeys narrow a primary match through Logic. Optional.
	SecondaryKeys []string
	Logic         lore.Logic
	// Order is priority under the tail budget when more fires than fits. It is
	// authored, never learned: "did this memory help" has no signal behind it.
	Order int
	// Text is the body — may be multi-line.
	Text string
	// Archived is when this entry entered the archive. Recorded because it is
	// knowable only at archive time and unrecoverable afterwards; nothing reads
	// it for retrieval today (decay ranks, never gates, and ranking is not
	// built), but an entry archived before the field existed could never grow
	// one.
	Archived time.Time
	// Scope is which archive this came from (ScopeProject / ScopeUser). Stamped
	// on load rather than stored in the file: the file's location already says
	// it, and a copy in the frontmatter could only ever disagree.
	Scope string

	path string // absolute file path; provenance, not identity
}

// Path is where this entry lives on disk, "" for an entry that has not been
// written (or an in-memory archive).
func (e ArchiveEntry) Path() string { return e.path }

// Ref is how an entry is named everywhere the model can see it: "project:the-id".
//
// Scope-qualified because the two archives are matched together into one tail
// block, so a bare id would be ambiguous exactly when both scopes hold something
// on the same subject — and because the scope is the other argument the model
// needs to act on it.
func (e ArchiveEntry) Ref() string {
	if e.Scope == "" {
		return e.ID
	}
	return e.Scope + ":" + e.ID
}

// Title is the entry's display name — its Name, or its ID when unnamed.
func (e ArchiveEntry) Title() string {
	if n := strings.TrimSpace(e.Name); n != "" {
		return n
	}
	return e.ID
}

// archiveFrontmatter is the WRITE side of an entry file. Reading goes through
// lore.ParseEntry so the two cannot drift on field names, defaults, or the
// logic vocabulary — and TestArchiveFilesRoundTripThroughLore pins that they
// don't, which is the only thing making one struct for writing and a different
// parser for reading safe.
type archiveFrontmatter struct {
	Name          string   `yaml:"name,omitempty"`
	Keys          []string `yaml:"keys"`
	SecondaryKeys []string `yaml:"secondary_keys,omitempty"`
	Logic         string   `yaml:"logic,omitempty"`
	Order         *int     `yaml:"order,omitempty"`
	Archived      string   `yaml:"archived,omitempty"`
}

// archiveExtras are the fields lore.ParseEntry does not know about, pulled from
// the same frontmatter in a second pass. yaml drops unknown keys silently, which
// is what makes the superset work in the first place.
type archiveExtras struct {
	Archived string `yaml:"archived"`
}

// Archive is one scope's archived memories: a directory of entry files, cached
// in memory and reloaded from disk on every mutation.
//
// Unlike Store there is no cross-process lock. Store needs one because two
// writers share ONE file and the loser's whole delta would be lost; here each
// entry is its own file, so concurrent writers to different entries do not
// interact at all. The one thing that CAN collide is minting an id, and that is
// handled where it happens — with O_EXCL, so the loser of a race sees the file
// already exists and takes the next suffix.
//
// An empty bound dir means in-memory only, matching Store: entries are accepted
// and listed for the session and then lost.
type Archive struct {
	mu    sync.Mutex
	scope string // ScopeProject / ScopeUser
	label string
	dir   string
	now   func() time.Time
	items []ArchiveEntry
	// problems are per-file load errors. Collected rather than fatal: one
	// hand-edited file with a typo must not take the whole archive offline, and
	// a silently skipped file is how an entry stops firing with nobody able to
	// say why.
	problems []string
	// seq disambiguates ids minted in the same nanosecond by an in-memory
	// archive, where there is no file to collide with.
	seq int
}

// NewArchive returns an unbound archive for a scope. Bind it with Rebind.
func NewArchive(scope, label string) *Archive {
	return &Archive{scope: scope, label: label, now: time.Now}
}

// Scope is which tier this archive belongs to (ScopeProject / ScopeUser).
func (a *Archive) Scope() string { return a.scope }

// Label is the model-facing name of this archive's scope.
func (a *Archive) Label() string { return a.label + " archive" }

// SetClock pins the timestamp source. Tests use it so a written file is
// byte-identical run to run — the swarm archive's id guard passed without its
// guard until its clock was pinned, which is the precedent this exists for.
func (a *Archive) SetClock(now func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if now != nil {
		a.now = now
	}
}

// Bound reports whether this archive has a directory to persist to.
func (a *Archive) Bound() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dir != ""
}

// Dir is the bound directory, "" when in-memory.
func (a *Archive) Dir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dir
}

// Rebind points the archive at a directory and loads it. A missing directory is
// not an error — the scope simply has nothing archived yet. An empty dir unbinds
// to in-memory mode. Binding to the already-bound dir is a no-op; use Reload to
// force a re-read.
func (a *Archive) Rebind(dir string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if dir == a.dir {
		return nil
	}
	a.dir = dir
	a.items, a.problems = nil, nil
	if dir == "" {
		return nil
	}
	return a.loadLocked()
}

// Reload re-reads the bound directory, picking up another instance's writes or a
// hand-edit. A no-op in in-memory mode, and nil-safe for the same reason Len is.
func (a *Archive) Reload() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dir == "" {
		return nil
	}
	return a.loadLocked()
}

// List returns the archived entries, newest first.
func (a *Archive) List() []ArchiveEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ArchiveEntry(nil), a.items...)
}

// Len is the number of archived entries. nil-safe: an unbound scope has a nil
// archive, and every caller that counts across both scopes would otherwise need
// the same two guards.
func (a *Archive) Len() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.items)
}

// Problems returns the per-file load errors from the last read: files that are
// present but could not be turned into entries. An entry in here is INERT — it
// occupies disk and never fires — so a surface that hides it is hiding the one
// failure this tier has no other symptom for.
func (a *Archive) Problems() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.problems...)
}

// Bytes is the archive's total size on disk, the number MaxArchiveBytes is
// measured against.
func (a *Archive) Bytes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bytesLocked()
}

func (a *Archive) bytesLocked() int {
	n := 0
	for _, e := range a.items {
		n += len(serializeArchive(e))
	}
	return n
}

// Add archives an entry, minting an id from its name or body and writing its
// file. The stored entry (with its id and timestamp filled in) is returned.
//
// Keys are required. An entry with no keys cannot activate, so storing one is
// storing something that is unreachable by every path except an explicit search
// — lore.ParseEntry treats the same shape as an error for the same reason.
func (a *Archive) Add(e ArchiveEntry) (ArchiveEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	body := strings.TrimSpace(e.Text)
	if body == "" {
		return ArchiveEntry{}, fmt.Errorf("entry text is required")
	}
	if n := len(body); n > MaxArchiveEntryBytes {
		return ArchiveEntry{}, fmt.Errorf("entry is %d bytes, over the %d limit for one archived memory — "+
			"split it, or put it in docs/ and archive a pointer to it (nothing was saved)", n, MaxArchiveEntryBytes)
	}
	keys := trimAll(e.Keys)
	if len(keys) == 0 {
		return ArchiveEntry{}, fmt.Errorf("keys are required: an archived memory with no keys can never fire, " +
			"so it would be reachable only by explicit search. Key it on what someone would TYPE when they need " +
			"this fact — the words in the question, not the identifiers in the answer")
	}
	e.Text, e.Keys, e.SecondaryKeys = body, keys, trimAll(e.SecondaryKeys)
	if e.Order == 0 {
		e.Order = lore.DefaultOrder
	}
	if e.Archived.IsZero() {
		e.Archived = a.now()
	}

	if err := a.reloadIfBoundLocked(); err != nil {
		return ArchiveEntry{}, err
	}
	if m := findSimilarArchive(a.items, body); m != nil {
		return ArchiveEntry{}, fmt.Errorf("already archived as [%s] %s — recall it to read it, or forget "+
			"that one first if this is meant to replace it", m.Ref(), m.Title())
	}
	if n := a.bytesLocked() + len(serializeArchive(e)); n > MaxArchiveBytes {
		return ArchiveEntry{}, a.fullError(fmt.Sprintf(
			"the archive would exceed its %d-byte budget (%d). It is read in full at every session start, "+
				"which is what the budget protects", MaxArchiveBytes, n))
	}

	stored, err := a.writeNewLocked(e)
	if err != nil {
		return ArchiveEntry{}, err
	}
	if a.dir == "" {
		a.items = append([]ArchiveEntry{stored}, a.items...)
		return stored, nil
	}
	if err := a.loadLocked(); err != nil {
		return ArchiveEntry{}, err
	}
	return stored, nil
}

// Remove deletes the archived entry identified by match (see Find) and returns
// what was deleted, so a caller can show the model exactly what it just lost.
func (a *Archive) Remove(match string) (ArchiveEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.reloadIfBoundLocked(); err != nil {
		return ArchiveEntry{}, err
	}
	e, err := a.findLocked(match)
	if err != nil {
		return ArchiveEntry{}, err
	}
	if e.path != "" {
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			return ArchiveEntry{}, err
		}
	}
	for i := range a.items {
		if a.items[i].ID == e.ID {
			a.items = append(a.items[:i], a.items[i+1:]...)
			break
		}
	}
	return e, nil
}

// Find resolves match to exactly one archived entry: an exact id first, then a
// unique case-insensitive substring of the id, name or body.
func (a *Archive) Find(match string) (ArchiveEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.findLocked(match)
}

// findLocked resolves match. An id wins outright so an entry whose id also
// appears inside another entry's body stays addressable.
func (a *Archive) findLocked(match string) (ArchiveEntry, error) {
	m := strings.TrimSpace(match)
	if m == "" {
		return ArchiveEntry{}, fmt.Errorf("match is required (an archived entry's id, or a unique substring of it)")
	}
	// Accept the scope-qualified form as well as the bare id. Every place the
	// model SEES an entry names it "project:the-id" — the recall block, the tool
	// replies — so refusing that form here would teach one spelling and accept
	// another. The scope is dropped rather than checked because this archive is
	// already the one the scope argument selected.
	if _, rest, ok := strings.Cut(m, ":"); ok && rest != "" {
		if a.scope == "" || strings.HasPrefix(strings.ToLower(m), strings.ToLower(a.scope)+":") {
			m = rest
		}
	}
	for _, e := range a.items {
		if strings.EqualFold(e.ID, m) {
			return e, nil
		}
	}
	lower := strings.ToLower(m)
	var hits []ArchiveEntry
	for _, e := range a.items {
		hay := strings.ToLower(e.ID + "\x00" + e.Name + "\x00" + e.Text)
		if strings.Contains(hay, lower) {
			hits = append(hits, e)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if len(a.items) == 0 {
			return ArchiveEntry{}, fmt.Errorf("nothing matches %q — %s is empty", m, a.Label())
		}
		// Same reasoning as Store.find: the model reached for something it
		// believed was there, and the useful reply is what IS there. Ids only,
		// because bodies are multi-line here and the full dump would bury it.
		return ArchiveEntry{}, fmt.Errorf("nothing matches %q. %s holds: %s",
			m, a.Label(), strings.Join(a.ids(), ", "))
	default:
		return ArchiveEntry{}, fmt.Errorf("%q matches %d archived entries (%s); use an id or a more specific substring",
			m, len(hits), strings.Join(idsOf(hits), ", "))
	}
}

func (a *Archive) ids() []string { return idsOf(a.items) }

func idsOf(items []ArchiveEntry) []string {
	out := make([]string, 0, len(items))
	for _, e := range items {
		out = append(out, e.ID)
	}
	return out
}

// Search ranks archived entries against a free-text query and returns the best
// up to limit.
//
// Deliberately NOT the keyword spec. Search exists for the two cases keyed
// firing does not cover — planning ("what do I know about X before X comes up")
// and the case where the spec simply missed — so scoring it through the same
// spec that missed would answer the second case with the same silence.
func (a *Archive) Search(query string, limit int) []ArchiveEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	q := strings.TrimSpace(query)
	if q == "" || len(a.items) == 0 {
		return nil
	}
	lower := strings.ToLower(q)
	qt := tokenSet(q)

	type scored struct {
		e ArchiveEntry
		s float64
	}
	var out []scored
	for _, e := range a.items {
		hay := strings.ToLower(e.ID + "\n" + e.Name + "\n" + strings.Join(e.Keys, " ") + "\n" +
			strings.Join(e.SecondaryKeys, " ") + "\n" + e.Text)
		s := overlap(qt, tokenSet(hay))
		if strings.Contains(hay, lower) {
			// A literal hit outranks any amount of scattered token overlap: the
			// caller typed a phrase, and an entry containing that phrase is a
			// better answer than one that happens to share vocabulary with it.
			s += 1
		}
		if s > 0 {
			out = append(out, scored{e, s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].s > out[j].s })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	res := make([]ArchiveEntry, 0, len(out))
	for _, s := range out {
		res = append(res, s.e)
	}
	return res
}

// overlap is the fraction of query tokens present in the haystack. Asymmetric on
// purpose: unlike jaccard (which dedup wants, comparing two entries) a long
// entry must not be penalised for containing more than the query asked about.
func overlap(query, hay map[string]struct{}) float64 {
	if len(query) == 0 {
		return 0
	}
	hit := 0
	for t := range query {
		if _, ok := hay[t]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(query))
}

// LoreEntries projects the archive into the matcher's shape.
//
// Built fresh per call rather than cached, so a mid-session archive/forget lands
// on the next turn with no rebuild. The Content carries the entry's id in a
// header because this block is the ONLY place a recalled memory is visible: the
// model has to be able to name what it just read in order to promote, correct or
// forget it, and a body with no handle can only be re-derived by searching for
// its own text.
func (a *Archive) LoreEntries() []lore.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]lore.Entry, 0, len(a.items))
	for _, e := range a.items {
		out = append(out, lore.Entry{
			Name:          e.Ref(),
			Keys:          e.Keys,
			SecondaryKeys: e.SecondaryKeys,
			Logic:         e.Logic,
			Constant:      false, // the entire point of the tier
			Order:         e.Order,
			Content:       renderRecalledEntry(e),
			Source:        "memory:" + e.Ref(),
			// An archived memory must not be re-triggered BY other recalled
			// content, and must not trigger further entries itself. Recursion is
			// how lore builds multi-hop activation over an implicit graph, and
			// this design has no graph yet — turning it on here would let one
			// memory's body pull in a second on incidental vocabulary, with no
			// weighting to stop the cascade.
			PreventRecursion: true,
			ExcludeRecursion: true,
		})
	}
	return out
}

// reloadIfBoundLocked refreshes the cache from disk before a mutation, so caps
// and lookups run against what is actually there rather than this process's
// possibly-stale view.
func (a *Archive) reloadIfBoundLocked() error {
	if a.dir == "" {
		return nil
	}
	return a.loadLocked()
}

// loadLocked reads every entry file in the bound directory. A missing directory
// is not an error.
func (a *Archive) loadLocked() error {
	ents, err := os.ReadDir(a.dir)
	if err != nil {
		if os.IsNotExist(err) {
			a.items, a.problems = nil, nil
			return nil
		}
		return err
	}
	var items []ArchiveEntry
	var problems []string
	for _, de := range ents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), archiveFileExt) {
			continue
		}
		path := filepath.Join(a.dir, de.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", de.Name(), err))
			continue
		}
		e, err := parseArchiveEntry(string(raw), path)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		e.Scope = a.scope
		items = append(items, e)
	}
	// Newest first, id as the tie-break so the order is total and stable — two
	// entries archived in the same second must not swap places between reads.
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].Archived.Equal(items[j].Archived) {
			return items[i].Archived.After(items[j].Archived)
		}
		return items[i].ID < items[j].ID
	})
	a.items, a.problems = items, problems
	return nil
}

// writeNewLocked mints an unused id for e and writes its file.
//
// The id race is settled by O_EXCL rather than by a lock: two processes
// archiving entries that slug the same would otherwise agree on a filename and
// one would overwrite the other. With O_EXCL the loser sees EEXIST and takes the
// next suffix, which is the whole reason this tier needs no lockfile.
func (a *Archive) writeNewLocked(e ArchiveEntry) (ArchiveEntry, error) {
	base := slugify(firstNonEmpty(e.Name, e.Text))
	e.Scope = a.scope
	if a.dir == "" {
		a.seq++
		e.ID = base
		if a.idTakenLocked(base) {
			e.ID = base + "-" + strconv.Itoa(a.seq)
		}
		return e, nil
	}
	if err := privfs.MkdirAll(a.dir); err != nil {
		return ArchiveEntry{}, err
	}
	for n := 1; n <= 999; n++ {
		id := base
		if n > 1 {
			id = base + "-" + strconv.Itoa(n)
		}
		path := filepath.Join(a.dir, id+archiveFileExt)
		f, err := privfs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return ArchiveEntry{}, err
		}
		e.ID, e.path = id, path
		if _, err := f.WriteString(serializeArchive(e)); err != nil {
			f.Close()
			_ = os.Remove(path)
			return ArchiveEntry{}, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(path)
			return ArchiveEntry{}, err
		}
		return e, nil
	}
	return ArchiveEntry{}, fmt.Errorf("could not mint an id for %q after 999 attempts", base)
}

func (a *Archive) idTakenLocked(id string) bool {
	for _, e := range a.items {
		if e.ID == id {
			return true
		}
	}
	return false
}

// fullError renders a budget refusal with the archive's contents and their
// sizes. Same reasoning as Store.fullError: a cap the model cannot act on
// without knowing what is stored costs a turn per refusal.
func (a *Archive) fullError(reason string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s; forget an entry first. %s holds:", reason, a.Label())
	for _, e := range a.items {
		fmt.Fprintf(&b, "\n  %s (%dB) %s", e.ID, len(serializeArchive(e)), e.Title())
	}
	return fmt.Errorf("%s", b.String())
}

// serializeArchive renders one entry to its on-disk form: YAML frontmatter, then
// the body.
func serializeArchive(e ArchiveEntry) string {
	fm := archiveFrontmatter{
		Name:          strings.TrimSpace(e.Name),
		Keys:          e.Keys,
		SecondaryKeys: e.SecondaryKeys,
		Logic:         logicName(e.Logic),
		Archived:      e.Archived.UTC().Format(time.RFC3339),
	}
	if e.Order != 0 && e.Order != lore.DefaultOrder {
		o := e.Order
		fm.Order = &o
	}
	// yaml.Marshal rather than hand-written lines: it emits struct fields in
	// declaration order (so diffs stay stable) and quotes what needs quoting,
	// which hand-writing a key list does not.
	front, err := yaml.Marshal(fm)
	if err != nil {
		// Cannot happen for this struct; degrade to a body-only file rather than
		// losing the memory, which is the thing worth keeping.
		return strings.TrimSpace(e.Text) + "\n"
	}
	return "---\n" + string(front) + "---\n\n" + strings.TrimSpace(e.Text) + "\n"
}

// logicName is the inverse of lore's logic vocabulary. and_any is the default
// and is written out anyway, because an entry file is meant to be edited and a
// field you can see is a field you can change.
func logicName(l lore.Logic) string {
	switch l {
	case lore.LogicNotAll:
		return "not_all"
	case lore.LogicNotAny:
		return "not_any"
	case lore.LogicAndAll:
		return "and_all"
	default:
		return "and_any"
	}
}

// parseArchiveEntry reads one entry file.
//
// The lore fields go through lore.ParseEntry rather than a second struct here,
// so the two tiers cannot disagree about what `logic: and_any` means or what an
// omitted `order` defaults to. The memory-only fields are pulled from the same
// frontmatter in a second pass, because yaml discards unknown keys — which is
// exactly what makes a superset frontmatter work.
func parseArchiveEntry(raw, path string) (ArchiveEntry, error) {
	le, ok, err := lore.ParseEntry(raw, path)
	if err != nil {
		return ArchiveEntry{}, err
	}
	if !ok {
		return ArchiveEntry{}, fmt.Errorf("memory archive %s: entry is disabled (enabled: false); "+
			"delete the file to forget it, or remove the flag to bring it back", filepath.Base(path))
	}
	// A constant archived entry is a contradiction, and an expensive one: it
	// would fire on EVERY turn from the uncached tail, paying per-turn forever
	// for what the active tier gets from the cached prefix at a cache-hit rate.
	// Promotion has a verb; it moves the entry to the other tier rather than
	// making this one behave like it.
	if le.Constant {
		return ArchiveEntry{}, fmt.Errorf("memory archive %s: `constant: true` is not valid here — an archived "+
			"entry that fires unconditionally costs its bytes on every turn. Promote it to active memory instead",
			filepath.Base(path))
	}

	var extra archiveExtras
	if front, _ := lore.SplitFrontmatter(raw); strings.TrimSpace(front) != "" {
		_ = yaml.Unmarshal([]byte(front), &extra)
	}
	archived, _ := time.Parse(time.RFC3339, strings.TrimSpace(extra.Archived))

	return ArchiveEntry{
		ID:            strings.TrimSuffix(filepath.Base(path), archiveFileExt),
		Name:          le.Name,
		Keys:          le.Keys,
		SecondaryKeys: le.SecondaryKeys,
		Logic:         le.Logic,
		Order:         le.Order,
		Text:          le.Content,
		Archived:      archived,
		path:          path,
	}, nil
}

// findSimilarArchive reports the archived entry that body duplicates.
//
// EXACT after normalization only — deliberately not the active tier's jaccard
// near-duplicate rule. That threshold (0.85 word overlap) was tuned for one-line
// facts, where high overlap really does mean the same fact twice. Applied to
// multi-kilobyte bodies it means something else entirely: two long procedures
// about neighbouring parts of one subsystem share most of their vocabulary and
// are still distinct, so the rule would start refusing exactly the detailed
// entries this tier exists to hold. It is also O(entries x body length) on every
// add, which at this tier's scale is the difference between an instant tool call
// and a visible pause.
func findSimilarArchive(items []ArchiveEntry, body string) *ArchiveEntry {
	nb := normalizeForDup(body)
	for i := range items {
		if normalizeForDup(items[i].Text) == nb {
			return &items[i]
		}
	}
	return nil
}

// slugify turns a title (or the opening of a body) into a filename stem.
//
// Letters and digits are kept as-is rather than restricted to ASCII: a memory
// written in another language would otherwise slug to nothing at all, and a run
// of entries called "entry-2", "entry-3" is a worse handle than a word the
// curator can read.
func slugify(s string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
			if b.Len() >= maxIDLen {
				return strings.Trim(b.String(), "-")
			}
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "memory"
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// trimAll drops blank entries and trims the rest. Mirrors lore's helper of the
// same name; duplicated rather than exported because it is three lines and
// exporting it would put a general-purpose name on lore's public surface.
func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
