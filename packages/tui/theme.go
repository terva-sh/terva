// Package tui is a small terminal UI: raw-mode input, differential
// line renderer, multi-line editor, and chat view. No external TUI
// framework; just ANSI escape codes.
package tui

import "strings"

// TerminalColor describes a terminal colour in one of the colour
// spaces terminals commonly support. Most of terva's theme still uses
// xterm-256 indexes, but user-bubble backgrounds can also use ANSI
// theme slots (for example SGR 100 / bright-black background) so they
// match the user's terminal theme rather than the fixed 256-colour
// cube.
type TerminalColor struct {
	Mode  terminalColorMode
	Index int
	R     int
	G     int
	B     int
}

type terminalColorMode int

const (
	terminalColor256 terminalColorMode = iota
	terminalColorANSI
	terminalColorRGB
)

func Color256(index int) TerminalColor { return TerminalColor{Mode: terminalColor256, Index: index} }
func ColorANSI(sgr int) TerminalColor  { return TerminalColor{Mode: terminalColorANSI, Index: sgr} }
func ColorRGB(r, g, b int) TerminalColor {
	return TerminalColor{Mode: terminalColorRGB, R: r, G: g, B: b}
}

// ANSI 256-color palette used by terva. Defined as numeric codes so we
// can swap themes without changing any render code.
type Theme struct {
	FG           int
	Muted        int
	Accent       int
	Background   *TerminalColor // optional full-row TUI background; nil keeps terminal default
	User         int            // label color for the user role
	UserBubbleBG TerminalColor  // background tint behind user message rows
	UserBubbleFG int            // foreground colour for user message rows
	Assistant    int            // label color for the terva role
	Tool         int
	ToolOut      int
	Error        int
	Warning      int
	Spinner      int // spinner + funny working line
	SelectionBG  int // background for highlighted rows
	SelectionFG  int // foreground for highlighted rows

	// MeterLow/Mid/High color the status bar's consumption meters
	// (context window, subscription usage) by stage: <70%, 70-90%,
	// >=90%. Staged whole-meter colors rather than per-cell gradients:
	// at 4-5 cells a gradient quantized to 256 colors reads as mud,
	// while a hue jump at the threshold survives color-impaired
	// vision. Zero values fall back to Muted/Warning/Error.
	MeterLow  int
	MeterMid  int
	MeterHigh int

	// StatusColors optionally recolors individual status-bar segments,
	// keyed by segment ID ("cwd", "git", "model", ...). Missing keys
	// keep the default muted rendering. Presentation only — which
	// segments render, and in what order, is status_line config, so an
	// extension-bundled theme can restyle the bar but never restructure
	// it.
	StatusColors map[string]int

	SpinnerFrames     []string
	SpinnerMessages   []string
	SpinnerIntervalMS int

	// Greetings are templated taglines for the startup headline (the text
	// after the "i'm …" prefix — "i'm terva." on the help/usage screen, or
	// "i'm <persona>." in the interactive welcome banner); FlavorVerbs/
	// FlavorNouns fill their {verb}/{noun} slots and any templated spinner
	// working lines. See flavor.go.
	Greetings   []string
	FlavorVerbs []string
	FlavorNouns []string

	SyntaxBaseStyle string
	Syntax          SyntaxTheme
}

// SyntaxTheme contains the chroma token colors used by code fences,
// file previews, and diffs. Values are chroma style entries, so they
// may include attributes after the color (for example "#81a1c1 bold").
type SyntaxTheme struct {
	Keyword             string
	KeywordConstant     string
	KeywordDeclaration  string
	KeywordNamespace    string
	KeywordReserved     string
	KeywordType         string
	NameBuiltin         string
	NameFunction        string
	NameClass           string
	NameDecorator       string
	LiteralString       string
	LiteralStringEscape string
	LiteralNumber       string
	Comment             string
	CommentPreproc      string
	Operator            string
	Punctuation         string
	Text                string
}

var nordSyntax = SyntaxTheme{
	Keyword:             "#81a1c1 bold",
	KeywordConstant:     "#81a1c1",
	KeywordDeclaration:  "#81a1c1",
	KeywordNamespace:    "#81a1c1",
	KeywordReserved:     "#81a1c1 bold",
	KeywordType:         "#88c0d0",
	NameBuiltin:         "#88c0d0",
	NameFunction:        "#8fbcbb",
	NameClass:           "#a3be8c bold",
	NameDecorator:       "#b48ead",
	LiteralString:       "#a3be8c",
	LiteralStringEscape: "#bf616a",
	LiteralNumber:       "#d08770",
	Comment:             "#616e88 italic",
	CommentPreproc:      "#b48ead",
	Operator:            "#eceff4",
	Punctuation:         "#d8dee9",
	Text:                "#e5e9f0",
}

var defaultSpinnerFrames = []string{
	"⠋",
	"⠙",
	"⠚",
	"⠞",
	"⠖",
	"⠦",
	"⠴",
	"⠲",
	"⠳",
	"⠓",
}

var defaultSpinnerMessages = []string{
	"thinking",
	"reticulating splines",
	"bribing the tokenizer",
	"asking the rubber duck",
	"summoning daemons",
	"consulting the oracle",
	"herding tokens",
	"compiling excuses",
	"poking the model",
	"negotiating with rate limits",
	"picking a fight with syntax",
	"reading between the bits",
	"tasting the semicolons",
	"pretending to understand the code",
	"petting the cache",
	"drafting clever replies",
	"warming up the GPU choir",
	"arguing with a stack trace",
	"googling the answer (not really)",
	"rewriting history",
	"wrangling the {noun}",
	"negotiating with the {noun}",
	"poking the {noun}",
	"every draft is a stone in the work",
	"bringing order to the unhewn",
	"finding the load-bearing measure",
	"where clarity grows, work grows lighter",
	"every correction serves the work",
}

var Dark = Theme{
	FG:                253,
	Muted:             244,
	Accent:            111,                  // soft blue
	User:              180,                  // warm tan (unused now that the speaker label is gone, kept for skin compat)
	UserBubbleBG:      ColorRGB(66, 69, 75), // #42454B
	UserBubbleFG:      248,                  // slightly lighter grey for readability on #42454B
	Assistant:         117,                  // bright cyan — the terva label color
	Tool:              114,                  // green
	ToolOut:           245,
	Error:             203,
	Warning:           214,
	Spinner:           183, // soft purple
	SelectionBG:       24,  // deep blue background
	SelectionFG:       231, // near-white foreground
	MeterLow:          244, // muted
	MeterMid:          214, // amber
	MeterHigh:         203, // red
	SpinnerFrames:     defaultSpinnerFrames,
	SpinnerMessages:   defaultSpinnerMessages,
	SpinnerIntervalMS: 80,
	Greetings:         defaultGreetings,
	FlavorVerbs:       defaultFlavorVerbs,
	FlavorNouns:       defaultFlavorNouns,
	SyntaxBaseStyle:   "monokai",
	Syntax:            nordSyntax,
}

// DarkDaltonized is the built-in color-vision-friendly dark theme. It
// re-bases every red/green semantic distinction on a blue/orange axis
// (the deuteranopia-safe standard): additions/success read blue, errors
// read vermillion-orange, and the status-bar meters climb a
// cyan -> amber -> magenta ramp whose stages differ in brightness as
// well as hue. Everything not carrying red/green semantics inherits
// the regular dark theme. (Syntax highlighting still carries some
// green/red string/escape tones — a fully daltonized syntax palette is
// a follow-up.)
var DarkDaltonized = func() Theme {
	t := Dark
	t.Tool = 39   // additions/success: vivid blue (was green)
	t.Error = 166 // errors/removals: vermillion orange (was red)
	t.Warning = 214
	t.MeterLow = 44   // cyan
	t.MeterMid = 214  // amber
	t.MeterHigh = 201 // magenta
	return t
}()

// LightDaltonized mirrors DarkDaltonized for light terminals: the same
// blue/orange re-basing of every red/green semantic slot, with shades
// dark enough to read on a pale background. The bare "daltonized"
// theme name resolves to whichever variant matches the detected
// terminal background.
var LightDaltonized = func() Theme {
	t := Light
	t.Tool = 26     // additions/success: deep blue (was green)
	t.Error = 166   // errors/removals: vermillion (was red)
	t.Warning = 130 // dark amber — moved off 166 so it stays distinct from Error
	t.MeterLow = 31 // deep teal
	t.MeterMid = 130
	t.MeterHigh = 127 // dark magenta
	return t
}()

var Light = Theme{
	FG:                236,
	Muted:             244,
	Accent:            33,
	User:              94,
	UserBubbleBG:      Color256(254), // very pale grey panel behind user rows on light theme
	UserBubbleFG:      240,           // dark grey text, legible on the pale panel
	Assistant:         31,            // deep cyan
	Tool:              28,
	ToolOut:           240,
	Error:             160,
	Warning:           166,
	Spinner:           91,  // purple
	SelectionBG:       153, // light blue
	SelectionFG:       232, // near-black
	MeterLow:          244, // muted
	MeterMid:          166, // amber
	MeterHigh:         160, // red
	SpinnerFrames:     defaultSpinnerFrames,
	SpinnerMessages:   defaultSpinnerMessages,
	SpinnerIntervalMS: 80,
	Greetings:         defaultGreetings,
	FlavorVerbs:       defaultFlavorVerbs,
	FlavorNouns:       defaultFlavorNouns,
	SyntaxBaseStyle:   "monokai",
	Syntax:            nordSyntax,
}

// FG256 wraps s in foreground color c using ANSI 256-color SGR.
func (t Theme) FG256(c int, s string) string {
	return sgrFG(c) + s + reset
}

// MeterColor returns the staged meter color for pct consumed: MeterLow
// below 70, MeterMid to 90, MeterHigh at or above. Zero slots fall
// back to Muted/Warning/Error so a bare Theme literal keeps the
// classic semantics.
func (t Theme) MeterColor(pct float64) int {
	low, mid, high := t.MeterLow, t.MeterMid, t.MeterHigh
	if low == 0 {
		low = t.Muted
	}
	if mid == 0 {
		mid = t.Warning
	}
	if high == 0 {
		high = t.Error
	}
	switch {
	case pct >= 90:
		return high
	case pct >= 70:
		return mid
	default:
		return low
	}
}

// StatusColor returns the theme's color for a status-bar segment, or
// fallback when the theme doesn't recolor that segment.
func (t Theme) StatusColor(id SegmentID, fallback int) int {
	if c, ok := t.StatusColors[string(id)]; ok {
		return c
	}
	return fallback
}

// BG256 wraps s in background color c using ANSI 256-color SGR.
// Useful when the visible cell needs a coloured fill but the
// underlying character should be a regular space (so mouse-copy
// from the terminal yields whitespace instead of a glyph).
func (t Theme) BG256(c int, s string) string {
	return sgrBG(c) + s + reset
}

// BG wraps s in a terminal background colour. Unlike BG256, this can
// target ANSI theme slots or truecolor RGB, which lets selected UI
// surfaces follow the user's terminal theme.
func (t Theme) BG(c TerminalColor, s string) string {
	return sgrBGColor(c) + s + reset
}

// FGColor wraps s in a terminal foreground colour. Unlike FG256, this can
// target ANSI theme slots or truecolor RGB — used so a persona's
// accent_color (#RRGGBB) can tint the welcome banner.
func (t Theme) FGColor(c TerminalColor, s string) string {
	return sgrFGColor(c) + s + reset
}

// AccentBar returns a 2-cell-wide leader: a coloured half-block
// glyph followed by a plain space gutter. Used as the speaker-label
// prefix in the chat ("▌ you", "▌ terva") and as the editor prompt so
// the bar reads consistently across the UI.
func (t Theme) AccentBar(c int) string {
	return t.FG256(c, "▌ ")
}

// Highlight paints s with the theme's selection colors (foreground +
// background). The caller is responsible for padding s to the desired
// width; styling alone does not extend the background past content.
func (t Theme) Highlight(s string) string {
	return t.SelectionStyle() + s + reset
}

// SelectionStyle returns the SGR prefix for the theme's selected row.
func (t Theme) SelectionStyle() string {
	return sgrFG(t.SelectionFG) + sgrBG(t.SelectionBG)
}

// BackgroundStyle returns the SGR prefix for the optional full-row
// TUI background. Empty means terva should leave the terminal's
// configured background untouched.
func (t Theme) BackgroundStyle() string {
	if t.Background == nil {
		return ""
	}
	return sgrBGColor(*t.Background)
}

// SelectionStyleFG returns the SGR prefix for selected-row text with
// a custom foreground. Useful for preserving semantic marks inside a
// highlighted row without hardcoding escape sequences outside theme.go.
func (t Theme) SelectionStyleFG(fg int) string {
	return sgrFG(fg) + sgrBG(t.SelectionBG)
}

// PadHighlight styles s and extends the selection background to the
// full terminal width so the highlight is a full row, not just a
// rectangle around the text.
func (t Theme) PadHighlight(s string, width int) string {
	visible := visibleWidth(s)
	if visible < width {
		s += strings.Repeat(" ", width-visible)
	}
	return t.SelectionStyle() + s + reset
}

// UserBubble paints a single user message row with the bubble
// background colour, padding to width so the tint extends to the
// full terminal width. Foreground stays in UserBubbleFG so text
// remains legible against the tint.
func (t Theme) UserBubble(s string, width int) string {
	visible := visibleWidth(s)
	if visible < width {
		s += strings.Repeat(" ", width-visible)
	}
	return sgrFG(t.UserBubbleFG) + sgrBGColor(t.UserBubbleBG) + s + reset
}

// UserBubbleRow renders one user-bubble row prefixed with a coloured
// half-block accent bar ("▌ ") so every line of the bubble has the
// terva-blue gutter at the very left. The bar lives outside the bubble
// tint (chat bg) so the bubble itself sits inside it. Width is the
// outer width including the bar; the bubble content is padded to
// width-2 (the bar + its trailing space).
func (t Theme) UserBubbleRow(content string, width int) string {
	// Bar plus a single space gutter, in the assistant accent colour
	// so it matches the tool-box / app accent and reads as terva's voice
	// marker. Two cells wide.
	bar := t.FG256(t.Assistant, "▌ ")
	bubbleW := width - 2
	if bubbleW < 1 {
		bubbleW = 1
	}
	return bar + t.UserBubble(content, bubbleW)
}

// Bold wraps s in bold SGR.
func Bold(s string) string { return "\x1b[1m" + s + "\x1b[22m" }

// Italic wraps s in italic SGR.
func Italic(s string) string { return "\x1b[3m" + s + "\x1b[23m" }

const reset = "\x1b[0m"

func sgrFG(c int) string { return "\x1b[38;5;" + itoa(c) + "m" }
func sgrBG(c int) string { return "\x1b[48;5;" + itoa(c) + "m" }
func sgrBGColor(c TerminalColor) string {
	switch c.Mode {
	case terminalColorANSI:
		return "\x1b[" + itoa(c.Index) + "m"
	case terminalColorRGB:
		return "\x1b[48;2;" + itoa(c.R) + ";" + itoa(c.G) + ";" + itoa(c.B) + "m"
	default:
		return sgrBG(c.Index)
	}
}

func sgrFGColor(c TerminalColor) string {
	switch c.Mode {
	case terminalColorANSI:
		return "\x1b[" + itoa(c.Index) + "m"
	case terminalColorRGB:
		return "\x1b[38;2;" + itoa(c.R) + ";" + itoa(c.G) + ";" + itoa(c.B) + "m"
	default:
		return sgrFG(c.Index)
	}
}

// small itoa to avoid pulling strconv into this hot path twice.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [4]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
