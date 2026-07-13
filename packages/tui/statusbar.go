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
	NoYolo     bool // confirmation mode enabled? (legacy; ApprovalMode supersedes)
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
)

// segmentFunc renders one segment into zero or more pre-styled atoms.
// Atoms are the wrap unit: the packer never splits inside one. nil
// means the segment has nothing to show and vanishes with its
// separators.
type segmentFunc func(p StatusBarParams) []string

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
}

// defaultStatusRows is the built-in layout: identity + spend on row 1
// (tree facts adjacent: git state beside the agent's own edit counts),
// meters on row 2, ambient state (approval/jail tags, chat bridge,
// extension segments) on row 3. Three semantic rows rather than two:
// a subscription provider puts several usage meters on row 2, and
// mixing the ambient tags in made it read cramped. Rows with no data
// vanish, so an idle session without tags is still two rows tall —
// and a row that still overflows wraps at segment boundaries rather
// than moving segments between rows, so membership never depends on
// width and nothing jumps around on resize. The immersive preset
// drops the workspace segments and leads with the persona instead of
// the raw provider/model pair. clock/session stay config-only.
func defaultStatusRows(hideWorkspace bool) [][]SegmentID {
	if hideWorkspace {
		return [][]SegmentID{
			{SegReplay, SegPersona, SegThinking, SegTokens, SegCost},
			{SegContext, SegUsage},
			{SegBridge, SegExt},
		}
	}
	return [][]SegmentID{
		{SegReplay, SegCWD, SegGit, SegEdits, SegModel, SegThinking, SegTokens, SegCost},
		{SegContext, SegUsage, SegSwarm},
		{SegTags, SegTasks, SegBridge, SegExt},
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

// StatusBar builds the status block shown above the editor: the
// configured (or default) rows of segments, greedily wrapped to Cols,
// with the busy spinner prefix glued to the first line when it fits.
func StatusBar(p StatusBarParams) []string {
	rows := make([][]string, 0, 2)
	for _, ids := range resolveStatusRows(p) {
		var atoms []string
		for _, id := range ids {
			if fn, ok := statusSegments[id]; ok {
				atoms = append(atoms, fn(p)...)
				continue
			}
			if txt, ok := p.ScriptSegments[string(id)]; ok {
				if atom := scriptAtom(p.Theme, id, txt); atom != "" {
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

// resolveStatusRows returns the configured row layout, filtered to
// known segment IDs (built-ins plus defined script names), or the
// per-mode defaults when no usable config exists. Unknown IDs are
// skipped (not errors) so configs survive segment renames and future
// additions gracefully. With no Rows config, defined scripts append
// to the last default row in stable (sorted) order so defining a
// script is enough to see it.
func resolveStatusRows(p StatusBarParams) [][]SegmentID {
	if len(p.Rows) > 0 {
		out := make([][]SegmentID, 0, len(p.Rows))
		for _, row := range p.Rows {
			var ids []SegmentID
			for _, s := range row {
				id := SegmentID(strings.ToLower(strings.TrimSpace(s)))
				_, builtin := statusSegments[id]
				_, script := p.ScriptSegments[string(id)]
				if builtin || script {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				out = append(out, ids)
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
	return rows
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

// layoutStatusRows greedily packs each row's atoms into terminal lines:
// atoms joined by " · " while they fit in Cols, continuation lines when
// they don't. The busy prefix occupies the head of the first line when
// it plus the first atom fit; otherwise it gets a line of its own —
// the same contract the previous bespoke layout kept.
func layoutStatusRows(p StatusBarParams, rows [][]string) []string {
	th := p.Theme
	sep := th.FG256(th.Muted, " · ")
	busyHead := ""
	if p.BusyPrefix != "" {
		busyHead = statusPad + p.BusyPrefix
	}

	var lines []string
	first := true
	for _, atoms := range rows {
		if len(atoms) == 0 {
			continue
		}
		head := statusPad
		if first && busyHead != "" {
			glued := busyHead + statusPad
			if p.Cols > 0 && visibleWidth(glued+atoms[0]) > p.Cols {
				lines = append(lines, busyHead)
			} else {
				head = glued
			}
		}
		first = false

		cur := head + atoms[0]
		for _, a := range atoms[1:] {
			cand := cur + sep + a
			if p.Cols > 0 && visibleWidth(cand) > p.Cols {
				lines = append(lines, cur)
				cur = statusPad + a
				continue
			}
			cur = cand
		}
		lines = append(lines, cur)
	}
	if len(lines) == 0 && busyHead != "" {
		return []string{busyHead}
	}
	return lines
}

// ---- segments ----

func segCWD(p StatusBarParams) []string {
	if p.HideWorkspace || p.CWD == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegCWD, th.Muted), abbreviatePath(shortenHome(p.CWD)))}
}

func segGit(p StatusBarParams) []string {
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
func segEdits(p StatusBarParams) []string {
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
func segPersona(p StatusBarParams) []string {
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
func segSwarm(p StatusBarParams) []string {
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
func segTasks(p StatusBarParams) []string {
	if s := strings.TrimSpace(p.TaskGlance); s != "" {
		return []string{p.Theme.FG256(p.Theme.StatusColor(SegTasks, p.Theme.Accent), s)}
	}
	return nil
}

// segReplay renders the session-player scrubber (Replay is pre-formatted by
// the caller). Absent outside `terva replay`. Leads the first row so the
// playback state reads first, in the accent colour to mark the distinct mode.
func segReplay(p StatusBarParams) []string {
	if p.Replay == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegReplay, th.Accent), p.Replay)}
}

// segSession names the live session file, for telling parallel
// terminals apart. Config-only (not in the default rows).
func segSession(p StatusBarParams) []string {
	if p.SessionName == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegSession, th.Muted), i18n.T("sess %s", p.SessionName))}
}

// segClock is a 24h wall clock. Config-only (not in the default rows);
// the minute-boundary refresh that keeps countdowns fresh keeps this
// fresh too.
func segClock(p StatusBarParams) []string {
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegClock, th.Muted), p.now().Format("15:04"))}
}

func segModel(p StatusBarParams) []string {
	if p.Provider == "" && p.Model == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegModel, th.Muted), fmt.Sprintf("(%s) %s", p.Provider, p.Model))}
}

func segThinking(p StatusBarParams) []string {
	label := thinkingLevelLabel(p.Reasoning)
	if label == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegThinking, th.Muted), i18n.T("thinking: %s", label))}
}

func segTokens(p StatusBarParams) []string {
	var parts []string
	if p.Usage.InputTokens > 0 {
		parts = append(parts, "↑"+formatTokens(p.Usage.InputTokens))
	}
	if p.Usage.OutputTokens > 0 {
		parts = append(parts, "↓"+formatTokens(p.Usage.OutputTokens))
	}
	if p.Usage.CacheReadTokens > 0 {
		parts = append(parts, "R"+formatTokens(p.Usage.CacheReadTokens))
	}
	if p.Usage.CacheWriteTokens > 0 {
		parts = append(parts, "W"+formatTokens(p.Usage.CacheWriteTokens))
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

func segCost(p StatusBarParams) []string {
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

func segContext(p StatusBarParams) []string {
	th := p.Theme
	used, max := p.ContextUsed, p.ContextMax
	if used <= 0 && max <= 0 {
		return nil
	}
	if max <= 0 {
		return []string{th.FG256(th.MeterColor(0), i18n.T("ctx %s", formatTokens(used)))}
	}
	pct := float64(used) / float64(max) * 100
	text := i18n.T("ctx %s/%s %s %d%%",
		formatTokens(used), formatTokens(max), meterBar(pct, 5), int(pct+0.5))
	if p.AutoCompacting {
		text += " " + i18n.T("(auto)")
	}
	return []string{th.FG256(th.MeterColor(pct), text)}
}

func segUsage(p StatusBarParams) []string {
	th := p.Theme
	now := p.now()
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
			text = fmt.Sprintf("%s %s %d%%", label, meterBar(w.UsedPercent, 4), int(w.UsedPercent+0.5))
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

func segTags(p StatusBarParams) []string {
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
	case p.NoYolo:
		atoms = append(atoms, th.FG256(color, i18n.T("yolo mode disabled")))
	}
	if p.Locked {
		atoms = append(atoms, th.FG256(color, i18n.T("jailed")))
	}
	return atoms
}

func segBridge(p StatusBarParams) []string {
	if p.ChatConnected == "" {
		return nil
	}
	th := p.Theme
	return []string{th.FG256(th.StatusColor(SegBridge, th.Muted), i18n.T("%s connected", p.ChatConnected))}
}

func segExt(p StatusBarParams) []string {
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

// meterBar renders pct as a cells-wide fill meter, rounding to the
// nearest cell like the /usage dialog's usageBar.
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
