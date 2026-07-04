package modes

import (
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// slashCommand is one entry in the autocomplete popup. Header rows
// (group dividers like "── extensions ───") are real entries
// flagged with header=true; they render but aren't navigable.
type slashCommand struct {
	Name   string // with leading "/" — also the text inserted/run on select
	Desc   string
	Header bool // true = visual divider, not selectable
	// Display, when set, is the popup label shown instead of Name — e.g. a
	// bare skill name for a `/skill <name>` argument suggestion, whose Name is
	// the full "/skill <name>" replacement. Selection still returns Name.
	Display string
}

// label is the text shown in the popup row for this entry.
func (c slashCommand) label() string {
	if c.Display != "" {
		return c.Display
	}
	return c.Name
}

// slashSuggester renders the popup that appears when the editor starts
// with "/". It does not own any input state — the editor drives.
const slashSuggestPageSize = 8

type slashSuggester struct {
	cursor int

	// jailed tracks whether the sandbox is currently locked. It is used
	// to hide state-dependent commands from the autocomplete popup.
	jailed bool

	// extra are commands contributed by extensions, refreshed each
	// frame from the extension manager. Empty when no extensions
	// have registered any. Sorted by name in SetExtra so map
	// iteration order doesn't reshuffle the popup between frames.
	extra []slashCommand

	// skills are the live skill names offered as `/skill <name>` argument
	// completions. Refreshed per render (cheap, in-memory) so a reload shows
	// up immediately. Sorted by name in SetSkills for stable popup order.
	skills []SkillCompletion

	// lastMatches is the list shown in the most recent Render call.
	// Up/Down read it so they know which indexes to skip across
	// header rows.
	lastMatches []slashCommand

	// maxRows caps how many match rows Render shows at once; the
	// window follows the cursor (cursorWindow). Set per-frame by the
	// redraw pass from the live terminal height — without it the
	// bare-"/" popup is taller than a 24-row terminal and its top
	// commands clip off-screen. 0 = no cap.
	maxRows int
}

// SetExtra updates the extension-contributed command list. Called
// once per render with the live snapshot from the extension manager.
// The list is sorted by name so the popup ordering stays stable
// across redraws (Manager.Commands() iterates a map, which Go
// randomises).
func (s *slashSuggester) SetExtra(cmds []slashCommand) {
	sorted := append([]slashCommand(nil), cmds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	s.extra = sorted
}

// SkillCompletion is one `/skill <name>` argument suggestion: a bare skill
// name and its one-line description. The suggester turns it into a
// "/skill <name>" replacement entry while the command's argument is typed.
type SkillCompletion struct {
	Name string
	Desc string
}

// SetSkills updates the skill names offered as `/skill <name>` argument
// completions. Sorted by name so the popup order is stable across redraws.
func (s *slashSuggester) SetSkills(sk []SkillCompletion) {
	sorted := append([]SkillCompletion(nil), sk...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	s.skills = sorted
}

// SetJailed updates the current sandbox state. Called once per render
// so state-dependent commands can appear/disappear immediately.
func (s *slashSuggester) SetJailed(jailed bool) { s.jailed = jailed }

// SetMaxRows caps the visible match window (0 = unlimited).
func (s *slashSuggester) SetMaxRows(n int) { s.maxRows = n }

// allCatalog returns slashCatalog plus the current extra commands
// (extension-registered) with a header divider between the two
// groups. Extra entries are only kept if they don't collide with
// a built-in name; the built-in always wins.
func (s *slashSuggester) allCatalog() []slashCommand {
	base := s.baseCatalog()
	if len(s.extra) == 0 {
		return base
	}
	out := make([]slashCommand, 0, len(base)+len(s.extra)+1)
	out = append(out, base...)
	var kept []slashCommand
	for _, c := range s.extra {
		dup := false
		for _, b := range base {
			if b.Name == c.Name {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, c)
		}
	}
	if len(kept) > 0 {
		out = append(out, slashCommand{Header: true, Name: "extensions"})
		out = append(out, kept...)
	}
	return out
}

// baseCatalog returns the built-in commands visible for the current
// interactive state.
func (s *slashSuggester) baseCatalog() []slashCommand {
	hide := "/unjail"
	if s.jailed {
		hide = "/jail"
	}
	catalog := builtinSlashCatalog()
	out := make([]slashCommand, 0, len(catalog)-1)
	for _, c := range catalog {
		if c.Name == hide {
			continue
		}
		out = append(out, c)
	}
	return out
}

// looksLikeSlashCommand reports whether text is an attempt at a slash
// command (valid or not). Returns true for things like "/foo" or
// "/bar baz" but false for paths ("/Users/pat/...") and regexes
// ("/foo.bar/") so those can be sent to the model as-is.
//
// The head after "/" must be a single simple word: only letters,
// digits, hyphens, and underscores. That excludes paths (contain "/"),
// regexes (contain "."), and URLs.
func looksLikeSlashCommand(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) < 2 || text[0] != '/' {
		return false
	}
	head := text[1:]
	if i := strings.IndexAny(head, " \t\n"); i >= 0 {
		head = head[:i]
	}
	if head == "" {
		return false
	}
	for _, r := range head {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func newSlashSuggester() *slashSuggester { return &slashSuggester{} }

// matches returns the commands whose name has input as a prefix.
// If input is just "/", everything is shown.
func (s *slashSuggester) matches(input string) []slashCommand {
	// Argument completion for `/skill <name>`: detected on the RAW input (the
	// trailing space matters — "/skill " means "offer every name") before the
	// command-name path below trims and rejects anything past the first space.
	if arg, ok := skillArgPrefix(input); ok {
		return s.skillMatches(arg)
	}
	input = strings.TrimRight(input, " ")
	if input == "" || !strings.HasPrefix(input, "/") {
		return nil
	}
	// If there is a space, the user has moved past the command name.
	if idx := strings.IndexByte(input, ' '); idx >= 0 {
		return nil
	}
	var out []slashCommand
	for _, c := range s.allCatalog() {
		if c.Header {
			// Headers ride along whenever there's at least one
			// matching command from their group; we drop trailing
			// orphan headers below.
			out = append(out, c)
			continue
		}
		if strings.HasPrefix(c.Name, input) {
			out = append(out, c)
		}
	}
	return pruneOrphanHeaders(out)
}

// skillArgPrefix reports whether input is the `/skill` command followed by a
// space and a name fragment still being typed, returning that fragment.
// It is NOT active for `/skills`, for `/skill` alone (command-name mode), or
// once a space follows the name (the user has moved on to the request).
func skillArgPrefix(input string) (arg string, ok bool) {
	const cmd = "/skill"
	if !strings.HasPrefix(input, cmd) {
		return "", false
	}
	rest := input[len(cmd):]
	if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false // "/skill" alone, or "/skills…" — complete the command name
	}
	arg = strings.TrimLeft(rest, " \t")
	if strings.ContainsAny(arg, " \t") {
		return "", false // name finished; user is typing the request now
	}
	return arg, true
}

// skillMatches builds the `/skill <name>` argument suggestions whose name has
// arg as a case-insensitive prefix. Each entry's Name is the full
// "/skill <name>" replacement (what Tab inserts / Enter runs); Display is the
// bare name shown in the popup.
func (s *slashSuggester) skillMatches(arg string) []slashCommand {
	arg = strings.ToLower(arg)
	out := make([]slashCommand, 0, len(s.skills))
	for _, sk := range s.skills {
		if arg == "" || strings.HasPrefix(strings.ToLower(sk.Name), arg) {
			out = append(out, slashCommand{
				Name:    "/skill " + sk.Name,
				Display: sk.Name,
				Desc:    sk.Desc,
			})
		}
	}
	return out
}

// pruneOrphanHeaders removes header rows that have no commands
// after them (i.e. the next non-header is missing or another
// header). Keeps the popup clean when the input filters out a whole
// group.
func pruneOrphanHeaders(in []slashCommand) []slashCommand {
	out := make([]slashCommand, 0, len(in))
	for i, c := range in {
		if c.Header {
			// A header owns only the run of commands up to the next
			// header. Keep it exactly when the entry right after it is
			// a command: scanning further ahead would let a header
			// whose whole group was filtered out ride on a later
			// group's matches (visible once the builtin catalog grew
			// multiple group dividers).
			if i+1 >= len(in) || in[i+1].Header {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// clampCursor keeps the cursor inside the current match list and
// nudges it past any header row so navigation never lands on one.
func (s *slashSuggester) clampCursor(n int) {
	if n <= 0 {
		s.cursor = 0
		return
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	if s.cursor >= n {
		s.cursor = n - 1
	}
}

// Up / Down navigate the suggestion list, skipping header rows in
// either direction so the cursor only ever lands on selectable
// commands.
func (s *slashSuggester) Up() {
	s.skipHeader(-1)
}
func (s *slashSuggester) Down() {
	s.skipHeader(+1)
}

func (s *slashSuggester) PageUp() {
	if len(s.lastMatches) == 0 {
		return
	}
	s.cursor -= slashSuggestPageSize
	if s.cursor < 0 {
		s.cursor = 0
	}
	if s.lastMatches[s.cursor].Header {
		s.skipHeader(+1)
	}
}

func (s *slashSuggester) PageDown() {
	if len(s.lastMatches) == 0 {
		return
	}
	s.cursor += slashSuggestPageSize
	if s.cursor >= len(s.lastMatches) {
		s.cursor = len(s.lastMatches) - 1
	}
	if s.lastMatches[s.cursor].Header {
		s.skipHeader(-1)
	}
}

// skipHeader moves the cursor by step, then keeps moving in the same
// direction across header rows until it lands on a real command (or
// hits the edge, in which case it bounces back to the nearest real
// row).
func (s *slashSuggester) skipHeader(step int) {
	list := s.lastMatches
	n := len(list)
	if n == 0 {
		return
	}
	s.cursor += step
	for s.cursor >= 0 && s.cursor < n && list[s.cursor].Header {
		s.cursor += step
	}
	if s.cursor < 0 {
		// Bounce: find the first non-header from the top.
		for i, c := range list {
			if !c.Header {
				s.cursor = i
				return
			}
		}
		s.cursor = 0
	}
	if s.cursor >= n {
		// Bounce: find the last non-header.
		for i := n - 1; i >= 0; i-- {
			if !list[i].Header {
				s.cursor = i
				return
			}
		}
		s.cursor = n - 1
	}
}

// Active reports whether the popup is visible for the given input.
func (s *slashSuggester) Active(input string) bool {
	return len(s.matches(input)) > 0
}

// Selection returns the currently highlighted command for input, or "".
// Headers are never returned even if the cursor index would point at
// one; the cursor is moved forward to the next real command.
func (s *slashSuggester) Selection(input string) string {
	m := s.matches(input)
	if len(m) == 0 {
		return ""
	}
	s.clampCursor(len(m))
	if m[s.cursor].Header {
		for i := s.cursor + 1; i < len(m); i++ {
			if !m[i].Header {
				s.cursor = i
				break
			}
		}
	}
	if m[s.cursor].Header {
		return ""
	}
	return m[s.cursor].Name
}

// Render returns the popup lines or nil.
func (s *slashSuggester) Render(input string, th tui.Theme, width int) []string {
	m := s.matches(input)
	if len(m) == 0 {
		return nil
	}
	s.lastMatches = m
	s.clampCursor(len(m))
	// Snap cursor off any header (e.g. after a filter change put it on one).
	if s.cursor >= 0 && s.cursor < len(m) && m[s.cursor].Header {
		for i := s.cursor + 1; i < len(m); i++ {
			if !m[i].Header {
				s.cursor = i
				break
			}
		}
	}
	// Compute the widest command name across the whole match list
	// (built-ins + extension-contributed) so every row's description
	// starts at the same x-position. A minimum keeps short lists
	// from collapsing the descriptions into the name column.
	nameWidth := 10
	for _, c := range m {
		if c.Header {
			continue
		}
		if n := len(c.label()); n > nameWidth {
			nameWidth = n
		}
	}
	// Window the matches around the cursor so the popup fits short
	// terminals and PageUp/PageDown visibly change what's shown.
	start, end := cursorWindow(s.cursor, len(m), s.maxRows)
	var lines []string
	if start > 0 {
		lines = append(lines, windowMoreAbove(th, start))
	}
	for i := start; i < end; i++ {
		c := m[i]
		if c.Header {
			// Breathing room around group dividers — a blank row
			// before AND after makes the boundary read at a glance.
			lines = append(lines, "")
			rule := strings.Repeat("─", width)
			label := "── " + c.Name + " "
			// runewidth, not len: the leading "── " glyphs are multi-byte,
			// so byte-length padding leaves the rule short of the right edge.
			if lw := runewidth.StringWidth(label); lw < width {
				rule = label + strings.Repeat("─", width-lw)
			}
			lines = append(lines, th.FG256(th.Muted, rule))
			lines = append(lines, "")
			continue
		}
		name := c.label()
		if len(name) < nameWidth {
			name = name + strings.Repeat(" ", nameWidth-len(name))
		}
		plain := "  " + name + "  " + c.Desc
		if i == s.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	if end < len(m) {
		lines = append(lines, windowMoreBelow(th, len(m), end))
	}
	// Blank row before the hint visually detaches it from the
	// command list and groups it with its trailing blank.
	lines = append(lines, "")
	lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("↑/↓ navigate - tab complete - enter run")))
	// Blank row after the hint separates the popup from the status
	// bar / editor below it.
	lines = append(lines, "")
	return lines
}

// Reset puts the cursor back to the first match. Call this whenever the
// input changes in a way that reshapes the match list.
func (s *slashSuggester) Reset() { s.cursor = 0 }
