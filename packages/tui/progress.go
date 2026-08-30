package tui

// OSC 9;4 progress reporting: "terva is busy" / "terva is idle", told to
// the terminal rather than only drawn on the screen.
//
// The sequence is ESC ] 9 ; 4 ; state ; percent ST, originally a ConEmu
// extension and since adopted by Windows Terminal and WezTerm. State 3 is
// "indeterminate" (a busy bar with no known end), state 0 removes the
// indicator. terva only ever needs those two: a turn's length is unknown
// until it ends, so there is no honest percentage to report.
//
// Two things this buys, and the second is the reason it exists:
//
//  1. A taskbar / tab progress indicator while a turn runs, so a terva
//     working in a background window says so without being looked at.
//
//  2. A structural busy/idle signal in a terminal recording. The spinner
//     is the only other "terva is working" marker in the byte stream, and
//     it is theme data: SpinnerFrames, SpinnerMessages and
//     SpinnerIntervalMS are all overridable (docs/themes.md), so tooling
//     that greps for a spinner phrase or glyph breaks the first time
//     someone records with a custom theme. These four bytes do not
//     change with the theme, which makes "when was the agent thinking?"
//     answerable by machine from a cast file alone.
//
// Why this defaults to OFF and auto-detects a SHORT allowlist, where OSC
// 8 hyperlinks can afford a generous one: an unknown terminal is not safe
// here. OSC 9 with no ";4" is iTerm2's "post a desktop notification"
// extension, and a terminal that implements THAT but not the progress
// sub-parameter treats our payload as notification text — so the failure
// mode is a toast reading "4;3;0" popping up on every turn, which is far
// worse than the nothing an unsupporting terminal does with OSC 8. Only
// terminals known to implement 9;4 are enabled by the sniff; everything
// else opts in with TERVA_PROGRESS=on.

import (
	"os"
	"strings"
	"sync/atomic"

	"terva.sh/terva/packages/envcompat"
)

// The two sequences terva emits. ST (ESC \) rather than BEL: both are
// accepted by every terminal that implements the extension, and ST is the
// form the ConEmu and Windows Terminal documentation specifies.
//
// The trailing "0" is the percent field, ignored for states 0 and 3 but
// expected to be present by some parsers.
const (
	// SeqProgressBusy sets an indeterminate progress indicator.
	SeqProgressBusy = "\x1b]9;4;3;0\x1b\\"
	// SeqProgressIdle removes the progress indicator.
	SeqProgressIdle = "\x1b]9;4;0;0\x1b\\"
)

// progressOn gates emission process-wide.
//
// Off by default and turned on explicitly by whoever owns the terminal
// (see DetectProgressSupport), for the same reason hyperlinksOn is: a
// lazily-detected default would make a test assert the machine it ran on,
// passing on a developer's WezTerm and failing on a bare CI runner.
var progressOn atomic.Bool

// SetProgress enables or disables OSC 9;4 emission process-wide.
func SetProgress(on bool) { progressOn.Store(on) }

// ProgressEnabled reports whether OSC 9;4 emission is on.
func ProgressEnabled() bool { return progressOn.Load() }

// ProgressBusy returns the sequence announcing an indeterminate busy
// state, or "" when progress reporting is off. Callers write the result
// unconditionally; "" is a no-op write.
func ProgressBusy() string {
	if !progressOn.Load() {
		return ""
	}
	return SeqProgressBusy
}

// ProgressIdle returns the sequence clearing the progress indicator, or
// "" when progress reporting is off.
//
// This must be emitted on the way out as well as on turn end: the
// indicator is terminal state, not screen content, so a terva that exits
// while busy leaves a stuck progress bar on the tab behind it.
func ProgressIdle() string {
	if !progressOn.Load() {
		return ""
	}
	return SeqProgressIdle
}

// DetectProgressSupport reports whether this terminal should be sent OSC
// 9;4 sequences. Call it once at startup and hand the answer to
// SetProgress.
//
// TERVA_PROGRESS=on|off overrides the sniff. "on" is also the documented
// way to force the signal into a recording (docs/recording.md) on a
// terminal the allowlist does not cover — the bytes land in the capture
// whether or not the live terminal does anything with them.
func DetectProgressSupport() bool {
	switch strings.ToLower(strings.TrimSpace(envcompat.Get("PROGRESS"))) {
	case "on", "1", "true", "yes", "always":
		return true
	case "off", "0", "false", "no", "none":
		return false
	}
	return detectProgressSupportAuto()
}

func detectProgressSupportAuto() bool {
	term := strings.ToLower(os.Getenv("TERM"))
	if term == "" || term == "dumb" {
		return false
	}
	// A multiplexer either swallows the sequence (tmux, without
	// allow-passthrough) or rewrites it (screen). Neither forwards it to
	// the outer terminal's taskbar, so the sniff would be claiming a
	// capability that does not reach anything.
	if os.Getenv("TMUX") != "" || os.Getenv("STY") != "" || strings.HasPrefix(term, "screen") {
		return false
	}
	// ConEmu / Windows Terminal / WezTerm implement 9;4. This list is
	// deliberately short — see the file comment on why an unrecognised
	// terminal is a notification hazard rather than a harmless no-op.
	// Notably absent: iTerm2 and Ghostty, which implement OSC 9 as a
	// desktop notification.
	if os.Getenv("ConEmuANSI") != "" || os.Getenv("ConEmuPID") != "" {
		return true
	}
	if os.Getenv("WT_SESSION") != "" {
		return true
	}
	if os.Getenv("WEZTERM_PANE") != "" || strings.ToLower(os.Getenv("TERM_PROGRAM")) == "wezterm" {
		return true
	}
	return false
}
