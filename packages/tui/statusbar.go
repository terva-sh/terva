package tui

// The status bar is a segment engine: named segments render themselves
// from StatusBarParams into pre-styled "atoms", an ordered row layout
// (config-overridable, per-mode defaults) selects and orders them, and
// a greedy packer wraps rows at atom boundaries on narrow terminals.
//
//	  ~/W/g/t/terva · ⎇ sothr-main* +499 -109 · (openai-codex) gpt-5.5 · thinking: high · ↑94k ↓1.8k · $0.529 ~$0.71/hr (sub)
//	  ctx 202k/272k ▓▓▓▓░ 74% · 5h ▓░░░ 15% ↻4h33m · wk ▓░░░ 8% ↻3d17h
//
// Design notes live in docs/proposals/tui-status-line.md. Segments that
// have no data return nil and drop silently, separators included; row
// membership never depends on width, so segments don't migrate between
// rows on resize.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// GitInfo is the status-bar view of the working repository, produced by
// the modes-side async prober. All fields are scalars so snapshots are
// comparable with == (the prober only invalidates the frame on change).
type GitInfo struct {
	// Present is true when the cwd is inside a git work tree and the
	// last probe succeeded. False renders the segment absent.
	Present bool
	// Branch is the checked-out branch name, or a short OID when HEAD
	// is detached.
	Branch string
	// Dirty is true when anything is staged, modified, or untracked.
	Dirty bool
	// Added / Removed are total line insertions/deletions vs HEAD
	// (staged + unstaged, tracked files only).
	Added   int
	Removed int
}

// StatusBarParams groups the many bits of state the status bar needs.
// Grew from a flat argument list once we settled on the layout.
type StatusBarParams struct {
	Theme      Theme
	Provider   string
	Model      string
	Reasoning  string // "" means thinking off
	Busy       bool
	BusyPrefix string // spinner + funny line when busy
	CWD        string
	Locked     bool // sandbox on?
	// ClassifierMode is the screening classifier's authority: "" / "off"
	// (none), "screen" (it may refuse a call but never permit one), or
	// "approve" (it answers on your behalf and the prompt never happens).
	// Renders beside the approval-mode tag because it modifies it rather
	// than replacing it: a classifier answers the prompts the MODE raised.
	ClassifierMode string

	// ApprovalMode is the live approval mode (plan/ask/auto-edit/yolo).
	// When non-empty it drives the tag instead of NoYolo; "yolo" shows
	// nothing (the default needs no badge).
	ApprovalMode string

	// Cumulative session usage and cost.
	Usage provider.Usage
	// Subscription is true when the credential is an OAuth token (claude
	// pro/max, chatgpt plus/pro) rather than a paid api key. We still
	// compute a cost for visibility and append "(sub)" so the user
	// knows no real money moved.
	Subscription bool

	// Last turn's input+cache tokens (approximates current live context).
	ContextUsed int
	ContextMax  int // model's context window; 0 disables the percentage

	// AutoCompacting is true when the agent is currently running a
	// model-triggered condense pass. Surfaces as "(auto)" after the
	// context percentage so it's clear where the spinner is coming from.
	AutoCompacting bool

	// ChatConnected names the connected chat bridge ("telegram",
	// "discord", ...), or "" when none.
	ChatConnected string

	// ExtStatus are short status segments contributed by extensions
	// (status_segment frames), shown as ambient atoms.
	ExtStatus []string

	// HideWorkspace suppresses the coding-context chrome — the working
	// directory path, the git segment, the sandbox "jailed" badge, and the
	// approval-mode tag — for the --chat / --play meta-modes, where there
	// is no workspace to speak of. It both selects the immersive default
	// rows and hard-gates those segments, so an explicit Rows config
	// naming "cwd" still shows nothing in an immersive session.
	HideWorkspace bool

	// UsageWindows are the current provider's subscription usage windows
	// (e.g. codex's 5h + weekly), each rendered as a meter with a reset
	// countdown. Full detail lives in /usage.
	UsageWindows []provider.UsageWindow

	// Git is the async prober's latest snapshot; zero value = absent.
	Git GitInfo

	// SwarmAgents counts live (running or pending) background swarm
	// agents; 0 renders the segment absent.
	SwarmAgents int

	// TaskGlance is the built-in task board's short status line (e.g.
	// "▸ Wiring the panel (2/5)"), computed by tasks.StatusGlance; empty
	// renders the segment absent (no tasks / not this mode).
	TaskGlance string

	// MemoryGlance is the durable-memory count (e.g. "🧠 7"); empty renders the
	// segment absent — memory switched off, nothing saved yet, or the cache not
	// yet filled. All three are honestly "nothing to show" rather than zero.
	MemoryGlance string

	// WorktreeGlance is the managed-worktree count line (e.g.
	// "worktrees 3 · 1 yours"), computed by worktree.StatusGlance from the
	// /worktree panel's cache; empty renders the segment absent (no
	// worktrees, or the panel has not been opened yet this session).
	WorktreeGlance string

	// SessionName is the short name of the live session file; empty
	// renders the segment absent.
	SessionName string

	// Replay is a pre-formatted session-player scrubber (e.g. "▶ 38%  2×"),
	// set only when the TUI is playing back a recording (`terva replay`);
	// empty renders the segment absent.
	Replay string

	// PersonaName/Emoji identify the active persona for the persona
	// segment; PersonaAccentRGB is its accent_color already parsed
	// (nil = no accent, render muted/themed). Immersive sessions use
	// this segment in place of the raw provider/model pair.
	PersonaName      string
	PersonaEmoji     string
	PersonaAccentRGB *TerminalColor

	// EditsAdded/Removed count lines the agent's edit/write tools have
	// changed this session (reset on the same epochs as the burn
	// rate). Distinct from Git's tree-state counts: this is what the
	// agent did, git is where the tree stands.
	EditsAdded   int
	EditsRemoved int

	// ScriptSegments carries the latest output of user-defined status
	// scripts, keyed by script name (already lowercased). A name
	// renders wherever Rows places it; with no Rows config, scripts
	// append to the last default row. Built-in segment IDs win a name
	// collision. Values render after sanitizeStatusScriptLine — SGR
	// styling passes through, cursor-moving bytes never do.
	ScriptSegments map[string]string

	// Rows overrides the segment layout: one list of segment IDs per
	// status row (see SegmentID constants). Unknown IDs are skipped;
	// nil or empty-after-filtering falls back to the per-mode defaults.
	Rows [][]string

	// Now is the clock used for reset countdowns and the burn rate.
	// Zero means time.Now(); tests inject a fixed instant.
	Now time.Time

	// SessionStart is when this run's cost meter epoch began, and
	// SessionCostBase the CostUSD already accrued at that instant (a
	// resumed session preloads historical cost, which must not count
	// toward the live burn rate). Zero SessionStart suppresses burn.
	SessionStart    time.Time
	SessionCostBase float64

	Cols int // terminal width; 0 disables wrapping

	// MaxWidth caps the content width: rows lay out inside
	// min(Cols, MaxWidth) so the bar stays a readable block on a very
	// wide terminal. 0 means uncapped (lay out to Cols).
	MaxWidth int

	// ReserveBusyRow keeps the busy line's row present (blank) while
	// idle, so the bottom band never changes height at turn
	// boundaries. Off by default: the busy row is transient.
	ReserveBusyRow bool
}

// effWidth is the width the layout packs into: Cols capped to
// MaxWidth. Cols 0 (wrapping disabled) still honours a cap, and both
// zero keeps the no-wrap behaviour.
func (p StatusBarParams) effWidth() int {
	switch {
	case p.MaxWidth <= 0:
		return p.Cols
	case p.Cols <= 0 || p.MaxWidth < p.Cols:
		return p.MaxWidth
	default:
		return p.Cols
	}
}

func (p StatusBarParams) now() time.Time {
	if p.Now.IsZero() {
		return time.Now()
	}
	return p.Now
}

// SegmentID names one status-bar segment for Rows configs.
type SegmentID string

const (
	SegCWD      SegmentID = "cwd"
	SegGit      SegmentID = "git"
	SegEdits    SegmentID = "edits"
	SegModel    SegmentID = "model"
	SegPersona  SegmentID = "persona"
	SegThinking SegmentID = "thinking"
	SegTokens   SegmentID = "tokens"
	SegCost     SegmentID = "cost"
	SegContext  SegmentID = "context"
	SegUsage    SegmentID = "usage"
	SegSwarm    SegmentID = "swarm"
	SegSession  SegmentID = "session"
	SegClock    SegmentID = "clock"
	SegTags     SegmentID = "tags"
	SegBridge   SegmentID = "bridge"
	SegExt      SegmentID = "ext"
	SegReplay   SegmentID = "replay"
	SegTasks    SegmentID = "tasks"
	SegWorktree SegmentID = "worktrees"
	SegMemory   SegmentID = "memory"

	// SegSpacer is a pseudo-segment: zero content, and at layout time
	// it absorbs the slack between the atoms before it and the atoms
	// after it, so a row can pin a group against the right edge of the
	// effective width. Several spacers in one row split the slack
	// evenly. It has no entry in statusSegments — the layout consumes
	// it before any segment renders.
	SegSpacer SegmentID = "spacer"
)

// segOpts is one segment's parsed per-segment options from the rows
// config ("tokens:io" → {io: ""}, "context:bar=10" → {bar: "10"}).
// Flags map to the empty string, key=value pairs to their value. nil
// means the entry carried no options; every accessor is nil-safe.
type segOpts map[string]string

// has reports whether the option was given, with or without a value.
func (o segOpts) has(name string) bool {
	_, ok := o[name]
	return ok
}

// intVal returns the option's positive integer value, or def when the
// option is absent, empty, or not a number — the silent tolerance the
// whole rows config keeps.
func (o segOpts) intVal(name string, def int) int {
	v, ok := o[name]
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// parseSegOpts parses the suffix after "id:": comma-separated flags
// or key=value pairs. Malformed tokens drop out silently — the same
// tolerance resolveStatusRows extends to unknown segment ids, so a
// config survives renames and older binaries.
func parseSegOpts(s string) segOpts {
	var o segOpts
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		k, v, _ := strings.Cut(tok, "=")
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if o == nil {
			o = segOpts{}
		}
		o[k] = strings.TrimSpace(v)
	}
	return o
}

// segmentFunc renders one segment into zero or more pre-styled atoms.
// Atoms are the wrap unit: the packer never splits inside one. nil
// means the segment has nothing to show and vanishes with its
// separators. The opts argument carries the entry's per-segment
// options; a segment that takes none ignores it.
type segmentFunc func(p StatusBarParams, o segOpts) []string

var statusSegments = map[SegmentID]segmentFunc{
	SegCWD:      segCWD,
	SegGit:      segGit,
	SegEdits:    segEdits,
	SegModel:    segModel,
	SegPersona:  segPersona,
	SegThinking: segThinking,
	SegTokens:   segTokens,
	SegCost:     segCost,
	SegContext:  segContext,
	SegUsage:    segUsage,
	SegSwarm:    segSwarm,
	SegSession:  segSession,
	SegClock:    segClock,
	SegTags:     segTags,
	SegBridge:   segBridge,
	SegExt:      segExt,
	SegReplay:   segReplay,
	SegTasks:    segTasks,
	SegWorktree: segWorktree,
	SegMemory:   segMemory,
}

// defaultStatusRows is the built-in layout. Three semantic rows, each
// with a left group and a right group around a spacer: row 1 is place
// (where am I, what tree state) left with the two glance segments —
// model and cost — pinned in the top-right corner; row 2 is the
// tanks, meters left and thinking effort + token counters right;
// row 3 is ambient state left (swarm is ambient: live background
// agents), glances right. Rows with no data vanish, and a row that
// overflows collapses its spacer and wraps at segment boundaries
// rather than moving segments between rows, so membership never
// depends on width and nothing jumps around on resize. The immersive
// preset drops the workspace segments and leads with the persona
// instead of the raw provider/model pair. clock/session stay
// config-only.
func defaultStatusRows(hideWorkspace bool) [][]SegmentID {
	if hideWorkspace {
		return [][]SegmentID{
			{SegReplay, SegPersona, SegSpacer, SegCost},
			{SegContext, SegUsage, SegSpacer, SegThinking, SegTokens},
			{SegBridge, SegExt},
		}
	}
	return [][]SegmentID{
		{SegReplay, SegCWD, SegGit, SegEdits, SegSpacer, SegModel, SegCost},
		{SegContext, SegUsage, SegSpacer, SegThinking, SegTokens},
		{SegTags, SegTasks, SegSwarm, SegSpacer, SegWorktree, SegMemory, SegBridge, SegExt},
	}
}

// DefaultStatusRows exposes the built-in row layout as plain strings,
// for settings code that computes toggled variants of the default.
func DefaultStatusRows(hideWorkspace bool) [][]string {
	rows := defaultStatusRows(hideWorkspace)
	out := make([][]string, len(rows))
	for r, ids := range rows {
		out[r] = make([]string, len(ids))
		for c, id := range ids {
			out[r][c] = string(id)
		}
	}
	return out
}

// statusPad is the fixed 2-space left inset every status line starts
// with, matching the editor prompt's inset so the bar lines up with the
// conversation column.
const statusPad = "  "

// statusTailPad is the matching right inset the flex layout keeps clear.
// It equals toolBoxOuterMargin, so a right-aligned spacer group ends on
// the same column as a tool box's closing corner. Without it the left
// edge lined up with the box and the right edge ran two cells past it.
const statusTailPad = toolBoxOuterMargin

// StatusBar builds the status block shown above the editor: the
// configured (or default) rows of segments, greedily wrapped to Cols,
// with the busy spinner prefix glued to the first line when it fits.
func StatusBar(p StatusBarParams) []string {
	rows := make([][]string, 0, 2)
	for _, segs := range resolveStatusRows(p) {
		var atoms []string
		for _, rs := range segs {
			if rs.id == SegSpacer {
				atoms = append(atoms, statusSpacerAtom)
				continue
			}
			if fn, ok := statusSegments[rs.id]; ok {
				atoms = append(atoms, fn(p, rs.opts)...)
				continue
			}
			if txt, ok := p.ScriptSegments[string(rs.id)]; ok {
				if atom := scriptAtom(p.Theme, rs.id, txt); atom != "" {
					atoms = append(atoms, atom)
				}
			}
		}
		if len(atoms) > 0 {
			rows = append(rows, atoms)
		}
	}
	return layoutStatusRows(p, rows)
}

// resolvedSeg is one entry of the resolved row layout: a segment id
// plus its parsed per-segment options ("tokens:io" → id tokens, opts
// {io}). opts is nil for a bare id.
type resolvedSeg struct {
	id   SegmentID
	opts segOpts
}

// resolveStatusRows returns the configured row layout, filtered to
// known segment IDs (built-ins plus defined script names), or the
// per-mode defaults when no usable config exists. Unknown IDs are
// skipped (not errors) so configs survive segment renames and future
// additions gracefully. With no Rows config, defined scripts append
// to the last default row in stable (sorted) order so defining a
// script is enough to see it.
//
// An entry can carry an options suffix: "id:opts", opts a
// comma-separated list of flags or key=value pairs. A script whose
// name itself contains ":" wins by exact match before the suffix
// parse, so no existing script breaks.
func resolveStatusRows(p StatusBarParams) [][]resolvedSeg {
	known := func(id SegmentID) bool {
		if _, builtin := statusSegments[id]; builtin || id == SegSpacer {
			return true
		}
		_, script := p.ScriptSegments[string(id)]
		return script
	}
	if len(p.Rows) > 0 {
		out := make([][]resolvedSeg, 0, len(p.Rows))
		for _, row := range p.Rows {
			var segs []resolvedSeg
			for _, s := range row {
				raw := strings.ToLower(strings.TrimSpace(s))
				if id := SegmentID(raw); known(id) {
					segs = append(segs, resolvedSeg{id: id})
					continue
				}
				if base, optsStr, ok := strings.Cut(raw, ":"); ok {
					if id := SegmentID(base); known(id) {
						segs = append(segs, resolvedSeg{id: id, opts: parseSegOpts(optsStr)})
					}
				}
			}
			if len(segs) > 0 {
				out = append(out, segs)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	rows := defaultStatusRows(p.HideWorkspace)
	if len(p.ScriptSegments) > 0 {
		names := make([]string, 0, len(p.ScriptSegments))
		for name := range p.ScriptSegments {
			if _, builtin := statusSegments[SegmentID(name)]; !builtin {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		last := len(rows) - 1
		for _, name := range names {
			rows[last] = append(rows[last], SegmentID(name))
		}
	}
	out := make([][]resolvedSeg, len(rows))
	for r, ids := range rows {
		out[r] = make([]resolvedSeg, len(ids))
		for c, id := range ids {
			out[r][c] = resolvedSeg{id: id}
		}
	}
	return out
}

// scriptAtom renders one user script's output as a segment atom. Plain
// text takes the muted default (or the theme's status_colors entry for
// the script's name); output carrying its own SGR styling passes
// through with a trailing reset so a script that leaves bold open
// can't bleed into the separators.
func scriptAtom(th Theme, id SegmentID, txt string) string {
	txt = sanitizeStatusScriptLine(txt)
	if txt == "" {
		return ""
	}
	if strings.ContainsRune(txt, 0x1b) {
		return txt + reset
	}
	return th.FG256(th.StatusColor(id, th.Muted), txt)
}

// sanitizeStatusScriptLine makes arbitrary script output safe to embed
// in a status row: tabs expand, SGR color sequences pass through, and
// every other escape or control byte — the class that desyncs the
// renderer's cursor tracking — is dropped. Only the first line
// survives.
func sanitizeStatusScriptLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			seq, next := scanCSI(s, i)
			if seq != "" && seq[len(seq)-1] == 'm' {
				b.WriteString(seq) // SGR styling is welcome
			}
			i = next
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\t':
			b.WriteString("  ")
		case r < 0x20 || r == 0x7f:
			// drop non-printing controls
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return strings.TrimSpace(b.String())
}

// scanCSI returns the CSI sequence starting at i (or "" when the
// escape isn't CSI) and the index just past whatever escape was there.
func scanCSI(s string, i int) (seq string, next int) {
	if i+1 >= len(s) {
		return "", len(s)
	}
	if s[i+1] != '[' {
		// Non-CSI escape (OSC, DCS, two-byte): skip it entirely using
		// the same walker the bash normalizer uses.
		return "", skipStatusEscape(s, i)
	}
	j := i + 2
	for j < len(s) {
		c := s[j]
		j++
		if c >= 0x40 && c <= 0x7e {
			return s[i:j], j
		}
	}
	return "", len(s)
}

// skipStatusEscape advances past a non-CSI escape sequence: OSC/DCS
// style strings run to BEL or ST, anything else is a two-byte escape.
func skipStatusEscape(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case ']', 'P', '_', '^', 'X':
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	default:
		return i + 2
	}
}

// statusSpacerAtom marks a spacer's position in a row's atom list. A
// NUL sentinel rather than a styled string: no segment renders one,
// and sanitizeStatusScriptLine strips control bytes from script
// output, so it cannot collide with content.
const statusSpacerAtom = "\x00spacer\x00"

// layoutStatusRows greedily packs each row's atoms into terminal lines:
// atoms joined by " · " while they fit in the effective width (Cols
// capped to MaxWidth), continuation lines when they don't. The busy
// prefix renders as its own line above the rows — v2 glued it to the
// head of row 1, which shifted every segment right at turn start and
// re-widened with the elapsed timer every second. The transient row
// costs one line of chat height while busy; row 1's segments never
// move again.
//
// A row with spacers first tries the flex layout: groups of atoms with
// the slack split evenly across the gaps. When that row does not fit,
// every spacer collapses to the ordinary separator and the greedy wrap
// takes over — membership never depends on width, and a narrow
// terminal renders exactly as it did before spacers existed.
func layoutStatusRows(p StatusBarParams, rows [][]string) []string {
	th := p.Theme
	width := p.effWidth()
	sep := th.FG256(th.Muted, " · ")
	busyHead := ""
	if p.BusyPrefix != "" {
		busyHead = statusPad + p.BusyPrefix
	}

	var lines []string
	switch {
	case busyHead != "":
		lines = append(lines, busyHead)
	case p.ReserveBusyRow:
		// The reserved row holds the busy line's place while idle, so
		// the chat viewport keeps a constant height across turns.
		lines = append(lines, "")
	}
	for _, rowAtoms := range rows {
		groups, hasSpacer := splitSpacerGroups(rowAtoms)
		atoms := rowAtoms
		if hasSpacer {
			// Collapsed form: the markers drop out and the greedy
			// wrap below sees the row exactly as a spacerless config.
			atoms = atoms[:0:0]
			for _, g := range groups {
				atoms = append(atoms, g...)
			}
		}
		if len(atoms) == 0 {
			continue
		}

		if hasSpacer && width > 0 {
			if line, ok := flexLine(statusPad, groups, sep, width, statusTailPad); ok {
				lines = append(lines, line)
				continue
			}
		}

		cur := statusPad + atoms[0]
		for _, a := range atoms[1:] {
			cand := cur + sep + a
			if width > 0 && visibleWidth(cand) > width {
				lines = append(lines, cur)
				cur = statusPad + a
				continue
			}
			cur = cand
		}
		lines = append(lines, cur)
	}
	return lines
}

// splitSpacerGroups splits a row's atoms at spacer markers into the
// runs of renderable atoms between them. hasSpacer reports whether any
// marker was present; a marker at the row's edge (or two adjacent
// markers) contributes an empty group, which the flex layout renders
// as a wider gap.
func splitSpacerGroups(atoms []string) (groups [][]string, hasSpacer bool) {
	cur := []string{}
	for _, a := range atoms {
		if a == statusSpacerAtom {
			hasSpacer = true
			groups = append(groups, cur)
			cur = []string{}
			continue
		}
		cur = append(cur, a)
	}
	groups = append(groups, cur)
	return groups, hasSpacer
}

// flexLine lays one row out with its spacers as flexible gaps: each
// group joins internally with the usual separator, and the slack up to
// width splits evenly across the gaps. Every gap is at least as wide
// as the separator it replaces, so ok is false exactly when the
// collapsed form of the row overflows too — the caller then falls back
// to the greedy wrap and the v2 invariants hold unchanged.
//
// tail is the number of cells the layout keeps clear at the right edge,
// mirroring head's inset. It comes out of the slack only, never out of
// the fit test: a row that fits flush still renders on one line, with a
// tail narrower than requested, rather than dropping to the greedy wrap.
func flexLine(head string, groups [][]string, sep string, width, tail int) (string, bool) {
	sepW := visibleWidth(sep)
	parts := make([]string, 0, len(groups))
	content := 0
	for _, g := range groups {
		s := strings.Join(g, sep)
		parts = append(parts, s)
		content += visibleWidth(s)
	}
	gaps := len(parts) - 1
	slack := width - visibleWidth(head) - content - gaps*sepW
	if gaps < 1 || slack < 0 {
		return "", false
	}
	if slack -= tail; slack < 0 {
		slack = 0
	}
	var b strings.Builder
	b.WriteString(head)
	for i, part := range parts {
		if i > 0 {
			gap := sepW + slack/gaps
			if i <= slack%gaps {
				gap++
			}
			b.WriteString(strings.Repeat(" ", gap))
		}
		b.WriteString(part)
	}
	return strings.TrimRight(b.String(), " "), true
}

// ---- segments ----

// segCWD renders the working directory, abbreviated. The "full"
// option ("cwd:full") skips the per-component abbreviation; the home
// shortening stays.
func segCWD(p StatusBarParams, o segOpts) []string {
	if p.HideWorkspace || p.CWD == "" {
		return nil
	}
	path := shortenHome(p.CWD)
	if !o.has("full") {
		path = abbreviatePath(path)
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegCWD, th.Muted), path)}
}

func segGit(p StatusBarParams, _ segOpts) []string {
	if p.HideWorkspace || !p.Git.Present || p.Git.Branch == "" {
		return nil
	}
	th := p.Theme
	text := "⎇ " + p.Git.Branch
	if p.Git.Dirty {
		text += "*"
	}
	atom := th.FG256(th.StatusColor(SegGit, th.Muted), text)
	if p.Git.Added > 0 {
		atom += " " + th.FG256(th.Tool, fmt.Sprintf("+%d", p.Git.Added))
	}
	if p.Git.Removed > 0 {
		atom += " " + th.FG256(th.Error, fmt.Sprintf("-%d", p.Git.Removed))
	}
	return []string{atom}
}

// segEdits sizes the agent's own work this session: lines added and
// removed by the edit/write tools. Distinct from segGit (tree state):
// this answers "what did the agent do", git answers "where the tree
// stands". Workspace machinery, so hidden in immersive modes.
func segEdits(p StatusBarParams, _ segOpts) []string {
	if p.HideWorkspace || (p.EditsAdded <= 0 && p.EditsRemoved <= 0) {
		return nil
	}
	th := p.Theme
	atom := th.FG256(th.StatusColor(SegEdits, th.Muted), "Δ")
	if p.EditsAdded > 0 {
		atom += " " + th.FG256(th.Tool, fmt.Sprintf("+%d", p.EditsAdded))
	}
	if p.EditsRemoved > 0 {
		atom += " " + th.FG256(th.Error, fmt.Sprintf("-%d", p.EditsRemoved))
	}
	return []string{atom}
}

// segPersona is the persona's face in the bar: emoji + name, tinted
// with the persona's accent color when it has one (exact RGB — the
// same treatment the welcome banner gives it). A theme's status_colors
// override wins over the accent so users keep the last word.
func segPersona(p StatusBarParams, _ segOpts) []string {
	if p.PersonaName == "" {
		return nil
	}
	th := p.Theme
	text := p.PersonaName
	if p.PersonaEmoji != "" {
		text = p.PersonaEmoji + " " + text
	}
	if c, ok := th.StatusColors[string(SegPersona)]; ok {
		return []string{th.FG256(c, text)}
	}
	if p.PersonaAccentRGB != nil {
		return []string{th.FGColor(*p.PersonaAccentRGB, text)}
	}
	return []string{th.FG256(th.Muted, text)}
}

// segSwarm surfaces live background agents so a running swarm is
// visible without opening /swarm. Absent at zero; hidden in immersive
// modes, where dispatched agents are cast members, not machinery.
func segSwarm(p StatusBarParams, _ segOpts) []string {
	if p.HideWorkspace || p.SwarmAgents <= 0 {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegSwarm, th.Muted), i18n.TN(p.SwarmAgents, "⛭ %d agent", "⛭ %d agents"))}
}

// segTasks renders the built-in task board's glance (the active task + a
// done/total count, "▸ …" — see tasks.StatusGlance). Absent when there are no
// tasks. Accent-coloured so the current task reads at a glance, like the
// terva-tasks extension's segment it replaces.
func segTasks(p StatusBarParams, _ segOpts) []string {
	if s := strings.TrimSpace(p.TaskGlance); s != "" {
		return []string{p.Theme.FG256(p.Theme.StatusColor(SegTasks, p.Theme.Accent), s)}
	}
	return nil
}

// segMemory renders the durable-memory count — how many facts terva is
// carrying into future sessions. Muted: it is context for the session, not a
// call to act on, and it should not compete with the task glance beside it.
func segMemory(p StatusBarParams, _ segOpts) []string {
	if s := strings.TrimSpace(p.MemoryGlance); s != "" {
		return []string{p.Theme.FG256(p.Theme.StatusColor(SegMemory, p.Theme.Muted), s)}
	}
	return nil
}

// segWorktree renders the managed-worktree glance (count + how many this
// session holds — see worktree.StatusGlance). Absent until the /worktree
// panel has populated the cache, and absent with zero worktrees — the same
// visibility the retired terva-git-worktree extension's segment had.
func segWorktree(p StatusBarParams, _ segOpts) []string {
	if s := strings.TrimSpace(p.WorktreeGlance); s != "" {
		return []string{p.Theme.FG256(p.Theme.StatusColor(SegWorktree, p.Theme.Muted), s)}
	}
	return nil
}

// segReplay renders the session-player scrubber (Replay is pre-formatted by
// the caller). Absent outside `terva replay`. Leads the first row so the
// playback state reads first, in the accent colour to mark the distinct mode.
func segReplay(p StatusBarParams, _ segOpts) []string {
	if p.Replay == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegReplay, th.Accent), p.Replay)}
}

// segSession names the live session file, for telling parallel
// terminals apart. Config-only (not in the default rows). The "short"
// option ("session:short") keeps only the part after the last "-" —
// the random hash that actually identifies the session — saving ~20
// cells of timestamp the clock already tells.
func segSession(p StatusBarParams, o segOpts) []string {
	if p.SessionName == "" {
		return nil
	}
	name := p.SessionName
	if o.has("short") {
		if i := strings.LastIndex(name, "-"); i >= 0 && i < len(name)-1 {
			name = name[i+1:]
		}
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegSession, th.Muted), i18n.T("sess %s", name))}
}

// segClock is a 24h wall clock. Config-only (not in the default rows);
// the minute-boundary refresh that keeps countdowns fresh keeps this
// fresh too.
func segClock(p StatusBarParams, _ segOpts) []string {
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegClock, th.Muted), p.now().Format("15:04"))}
}

func segModel(p StatusBarParams, _ segOpts) []string {
	if p.Provider == "" && p.Model == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegModel, th.Muted), fmt.Sprintf("(%s) %s", p.Provider, p.Model))}
}

func segThinking(p StatusBarParams, _ segOpts) []string {
	label := thinkingLevelLabel(p.Reasoning)
	if label == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegThinking, th.Muted), i18n.T("thinking: %s", label))}
}

// segTokens renders the cumulative token counters. The "io" option
// ("tokens:io") keeps only ↑input ↓output — the cache totals stay in
// /usage. Full stays the default: subscription users watch the cache
// totals (review decision, 2026-09-04).
func segTokens(p StatusBarParams, o segOpts) []string {
	var parts []string
	if p.Usage.InputTokens > 0 {
		parts = append(parts, "↑"+formatTokens(p.Usage.InputTokens))
	}
	if p.Usage.OutputTokens > 0 {
		parts = append(parts, "↓"+formatTokens(p.Usage.OutputTokens))
	}
	if !o.has("io") {
		if p.Usage.CacheReadTokens > 0 {
			parts = append(parts, "R"+formatTokens(p.Usage.CacheReadTokens))
		}
		if p.Usage.CacheWriteTokens > 0 {
			parts = append(parts, "W"+formatTokens(p.Usage.CacheWriteTokens))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegTokens, th.Muted), strings.Join(parts, " "))}
}

// burnMinElapsed is how long a session must have run before the cost
// segment shows a $/hr burn rate; extrapolating from the first few
// minutes swings wildly.
const burnMinElapsed = 10 * time.Minute

func segCost(p StatusBarParams, _ segOpts) []string {
	if p.Usage.CostUSD <= 0 && !p.Subscription {
		return nil
	}
	text := fmt.Sprintf("$%.3f", p.Usage.CostUSD)
	if rate, ok := burnRate(p); ok {
		text += fmt.Sprintf(" ~$%.2f/hr", rate)
	}
	if p.Subscription {
		text += " " + i18n.T("(sub)")
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegCost, th.Muted), text)}
}

// burnRate is the live $/hr spend: cost accrued since this run's epoch
// over elapsed time. The epoch base subtracts the historical cost a
// resumed session preloads — dividing a week-old total by minutes since
// launch would show absurd rates.
func burnRate(p StatusBarParams) (float64, bool) {
	if p.SessionStart.IsZero() {
		return 0, false
	}
	elapsed := p.now().Sub(p.SessionStart)
	if elapsed < burnMinElapsed {
		return 0, false
	}
	delta := p.Usage.CostUSD - p.SessionCostBase
	if delta <= 0 {
		return 0, false
	}
	return delta / elapsed.Hours(), true
}

// segContext renders the context gauge. "context:bar=N" pins the
// meter to N cells and bypasses the width tiers.
func segContext(p StatusBarParams, o segOpts) []string {
	th := p.Theme
	used, max := p.ContextUsed, p.ContextMax
	if used <= 0 && max <= 0 {
		return nil
	}
	if max <= 0 {
		return []string{th.FG256(th.MeterColor(0), i18n.T("ctx %s", formatTokens(used)))}
	}
	cells, _ := meterCells(p.effWidth())
	cells = min(o.intVal("bar", cells), maxMeterCells)
	pct := float64(used) / float64(max) * 100
	text := i18n.T("ctx %s/%s %s %d%%",
		formatTokens(used), formatTokens(max), meterBar(pct, cells), int(pct+0.5))
	if p.AutoCompacting {
		text += " " + i18n.T("(auto)")
	}
	return []string{th.FG256(th.MeterColor(pct), text)}
}

// segUsage renders one meter per provider usage window.
// "usage:bar=N" pins the meters to N cells and bypasses the width
// tiers.
func segUsage(p StatusBarParams, o segOpts) []string {
	th := p.Theme
	now := p.now()
	_, cells := meterCells(p.effWidth())
	cells = min(o.intVal("bar", cells), maxMeterCells)
	var atoms []string
	for _, w := range p.UsageWindows {
		label := shortWindowLabel(w.Label)
		var text string
		color := th.Muted
		if w.UsedPercent < 0 {
			// Window exists but the provider reports no usable
			// percentage; mirror the /usage dialog's "?" convention.
			text = label + " ?"
		} else {
			text = fmt.Sprintf("%s %s %d%%", label, meterBar(w.UsedPercent, cells), int(w.UsedPercent+0.5))
			color = th.MeterColor(w.UsedPercent)
		}
		if !w.ResetsAt.IsZero() {
			if d := w.ResetsAt.Sub(now); d > 0 {
				text += " ↻" + formatCountdown(d)
			}
		}
		atoms = append(atoms, th.FG256(color, text))
	}
	return atoms
}

func segTags(p StatusBarParams, _ segOpts) []string {
	if p.HideWorkspace {
		return nil
	}
	th := p.Theme
	color := th.StatusColor(SegTags, th.Muted)
	var atoms []string
	switch {
	case p.ApprovalMode == "yolo":
		// Yolo runs every tool — foreign side-effecting ones included —
		// without asking. Render it in the warning color so the riskiest
		// posture is never the one segment with no badge.
		atoms = append(atoms, th.FG256(th.Warning, i18n.T("%s mode", p.ApprovalMode)))
	case p.ApprovalMode != "":
		atoms = append(atoms, th.FG256(color, i18n.T("%s mode", p.ApprovalMode)))
	}
	// The classifier tag sits next to the mode it modifies. Approve gets the
	// warning colour for the same reason yolo does: it is the posture where a
	// tool call can run without a person ever seeing it, and the riskiest
	// posture must never be the one with no badge. The two are distinguished
	// by the bang as well as the colour, so they still read apart in a
	// screenshot, a log, or a terminal with no colour at all.
	switch p.ClassifierMode {
	case "approve":
		atoms = append(atoms, th.FG256(th.Warning, i18n.T("⚖! approving")))
	case "screen":
		atoms = append(atoms, th.FG256(color, i18n.T("⚖ screened")))
	}
	if p.Locked {
		atoms = append(atoms, th.FG256(color, i18n.T("jailed")))
	}
	return atoms
}

func segBridge(p StatusBarParams, _ segOpts) []string {
	if p.ChatConnected == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegBridge, th.Muted), i18n.T("%s connected", p.ChatConnected))}
}

func segExt(p StatusBarParams, _ segOpts) []string {
	th := p.Theme
	color := th.StatusColor(SegExt, th.Muted)
	var atoms []string
	for _, seg := range p.ExtStatus {
		if s := strings.TrimSpace(seg); s != "" {
			atoms = append(atoms, th.FG256(color, s))
		}
	}
	return atoms
}

// ---- helpers ----

// abbreviatePath shortens every intermediate path component to its
// first rune (two runes for dot-dirs, so ".config" reads ".c" and not a
// bare dot), keeping the first and last components whole:
//
//	~/Workspace/forge.example.com/terva-sh/terva -> ~/W/f/t/terva
//
// Splits on "/" — the same Unix-only assumption shortenHome already
// makes when it joins home with "/".
func abbreviatePath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	for i := 1; i < len(parts)-1; i++ {
		r := []rune(parts[i])
		switch {
		case len(r) == 0:
			// keep empty components (leading slash artifacts) as-is
		case r[0] == '.' && len(r) >= 2:
			parts[i] = string(r[:2])
		default:
			parts[i] = string(r[:1])
		}
	}
	return strings.Join(parts, "/")
}

// meterCells picks the meter widths for the effective width, in tiers
// rather than a formula so the sizes are enumerable in a test. Tier
// boundaries only ever move a bar's width, never a segment between
// rows, so the no-migration rule holds. Width 0 (no width
// information) keeps the pre-v3 sizes.
func meterCells(width int) (ctx, usage int) {
	switch {
	case width >= 140:
		return 12, 8
	case width >= 100:
		return 8, 6
	default:
		return 5, 4
	}
}

// maxMeterCells bounds a bar=N pin: a misconfigured N in the
// thousands must not become the whole row.
const maxMeterCells = 40

// meterBar renders pct as a cells-wide fill meter, rounding to the
// nearest cell like the /usage dialog's usageBar. A meter that is in
// use but rounds to zero filled cells renders ▒ in its first cell, so
// a 2% window and an untouched one stop being the same glyphs.
func meterBar(pct float64, cells int) string {
	if cells <= 0 {
		return ""
	}
	switch {
	case pct < 0:
		pct = 0
	case pct > 100:
		pct = 100
	}
	filled := min(int(pct/100*float64(cells)+0.5), cells)
	if filled == 0 && pct > 0 {
		return "▒" + strings.Repeat("░", cells-1)
	}
	return strings.Repeat("▓", filled) + strings.Repeat("░", cells-filled)
}

// formatCountdown renders a duration-until-reset in the largest two
// units, floored to minutes: "3d17h", "4h33m", "12m", "<1m".
func formatCountdown(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	mins := int(d.Minutes())
	days := mins / (24 * 60)
	mins -= days * 24 * 60
	hours := mins / 60
	mins -= hours * 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// shortWindowLabel compacts common provider window names for the bar;
// anything unrecognized passes through verbatim.
func shortWindowLabel(label string) string {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "weekly", "week":
		return "wk"
	case "monthly", "month":
		return "mo"
	}
	return label
}

func thinkingLevelLabel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "off", "none", "no", "false", "disabled":
		return ""
	case "minimum", "minimal", "min":
		return "minimal"
	case "maximum", "xhigh":
		return "maximum"
	case "max":
		return "max"
	default:
		return strings.ToLower(strings.TrimSpace(level))
	}
}
