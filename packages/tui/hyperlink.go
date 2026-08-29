package tui

// OSC 8 hyperlinks.
//
// A terminal wraps a long line by pushing the overflow onto the next
// row; terva wraps it by emitting a real newline, because it has to
// know how many rows a thing occupies before it draws it. The two look
// identical on screen and are not identical at all to a mouse: the
// terminal can rejoin its own soft wrap when you select across it, and
// cannot rejoin ours. That is why a login URL long enough to wrap
// stopped being clickable and came out of the clipboard in pieces.
//
// OSC 8 fixes it without touching the layout. The sequence carries the
// URL out of band -- ESC ] 8 ; params ; URI ST, then the visible text,
// then an empty ESC ] 8 ; ; ST to close -- so the terminal knows the
// target regardless of where the visible text happens to break. The
// `id=` parameter is what makes a wrapped link one link: runs sharing an
// id are the same hyperlink even when rows separate them.
//
// Terminals that do not implement OSC 8 ignore the sequence and draw the
// text, so the fallback is the pre-existing rendering, byte for byte.
// Terminals that mangle unknown OSC sequences (multiplexers, mostly) are
// excluded by DetectHyperlinkSupport instead of being relied on to cope.

import (
	"hash/fnv"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"terva.sh/terva/packages/envcompat"
)

// hyperlinkClose ends an OSC 8 hyperlink. Emitting it when no link is
// open is a no-op on every terminal that implements the extension, which
// is what lets the truncate path close a link it may have cut in half
// without first proving there was one.
const hyperlinkClose = "\x1b]8;;\x1b\\"

// HyperlinkClose is the exported form of hyperlinkClose, for renderers
// outside this package that emit a link and must be able to close it.
const HyperlinkClose = hyperlinkClose

// hyperlinksOn gates emission process-wide.
//
// It defaults to OFF and is turned on explicitly by whoever owns the
// terminal (see DetectHyperlinkSupport), rather than being auto-detected
// from the environment at the point of use. A lazily-detecting default
// would make every rendering test in the tree behave one way on a
// developer's iTerm2 and another way on a CI runner with no TERM_PROGRAM
// -- the tests would be asserting the machine they ran on. Off by
// default means goldens are stable everywhere and a test that wants the
// hyperlink path says so.
var hyperlinksOn atomic.Bool

// SetHyperlinks enables or disables OSC 8 emission process-wide.
func SetHyperlinks(on bool) { hyperlinksOn.Store(on) }

// HyperlinksEnabled reports whether OSC 8 emission is on.
func HyperlinksEnabled() bool { return hyperlinksOn.Load() }

// maxHyperlinkURL bounds the URI terva will put in an OSC 8 sequence.
// Link targets come out of model prose as well as our own dialogs, and a
// pathological "URL" of unbounded length would ride in every frame of
// the diff for no benefit. Real links are far below this.
const maxHyperlinkURL = 2048

// DetectHyperlinkSupport reports whether this terminal should be sent
// OSC 8 sequences. Call it once at startup and hand the answer to
// SetHyperlinks.
//
// TERVA_HYPERLINKS=on|off overrides the sniff, because detection by
// environment variable is a heuristic and someone will always be on a
// terminal it guesses wrong about.
func DetectHyperlinkSupport() bool {
	switch strings.ToLower(strings.TrimSpace(envcompat.Get("HYPERLINKS"))) {
	case "on", "1", "true", "yes", "always":
		return true
	case "off", "0", "false", "no", "none":
		return false
	}
	return detectHyperlinkSupportAuto()
}

func detectHyperlinkSupportAuto() bool {
	term := strings.ToLower(os.Getenv("TERM"))
	if term == "" || term == "dumb" {
		return false
	}
	// Inside tmux or screen the sequence has to survive a multiplexer
	// that rewrites the stream. tmux only passes OSC 8 through in recent
	// versions and screen mangles it, and the failure mode is a URL
	// smeared across the transcript as literal text -- much worse than
	// the unclickable-but-readable status quo. Opt in with
	// TERVA_HYPERLINKS=on if your multiplexer handles it.
	if os.Getenv("TMUX") != "" || os.Getenv("STY") != "" || strings.HasPrefix(term, "screen") {
		return false
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "iterm.app", "wezterm", "ghostty", "vscode", "hyper", "rio", "tabby", "warpterminal":
		return true
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("WEZTERM_PANE") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return true
	}
	if strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") || strings.Contains(term, "wezterm") {
		return true
	}
	// Windows Terminal.
	if os.Getenv("WT_SESSION") != "" {
		return true
	}
	// VTE (GNOME Terminal, Tilix, ...) gained OSC 8 in 0.50.
	if v, err := strconv.Atoi(os.Getenv("VTE_VERSION")); err == nil && v >= 5000 {
		return true
	}
	return false
}

// Hyperlink wraps text in an OSC 8 hyperlink pointing at url, deriving
// the link id from the url. Returns text unchanged when hyperlinks are
// off or url is not something we are willing to put on the wire.
func Hyperlink(url, text string) string {
	return HyperlinkID(url, hyperlinkIDFor(url, 0), text)
}

// HyperlinkID is Hyperlink with the link id spelled out. Callers that
// split one link's visible text across several strings -- a URL wrapped
// over three rows, say -- must pass the SAME id for every piece, or the
// terminal treats them as three separate links and hovering one lights
// up a third of it.
func HyperlinkID(url, id, text string) string {
	if !hyperlinksOn.Load() || !safeHyperlinkURL(url) {
		return text
	}
	var b strings.Builder
	b.Grow(len(url) + len(text) + len(id) + 24)
	b.WriteString("\x1b]8;")
	if id != "" {
		b.WriteString("id=")
		b.WriteString(id)
	}
	b.WriteString(";")
	b.WriteString(url)
	b.WriteString("\x1b\\")
	b.WriteString(text)
	b.WriteString(hyperlinkClose)
	return b.String()
}

// safeHyperlinkURL rejects a target that cannot go inside an OSC
// sequence. The payload is terminated by BEL or ST, so a control byte in
// the URL would end the sequence early and spill the remainder onto the
// screen as text -- and link targets reach us from model output, not only
// from our own dialogs, so this is an injection boundary rather than a
// tidiness check.
func safeHyperlinkURL(url string) bool {
	if url == "" || len(url) > maxHyperlinkURL {
		return false
	}
	for i := 0; i < len(url); i++ {
		if url[i] < 0x20 || url[i] == 0x7f {
			return false
		}
	}
	return true
}

// hyperlinkIDFor derives a stable link id from the URL and an
// occurrence index. Stable because the wrap path re-emits the opening
// sequence verbatim on continuation rows, and the id is what tells the
// terminal those rows are one link; derived rather than counted so the
// same input renders the same bytes in a test.
func hyperlinkIDFor(url string, occurrence int) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(url))
	return strconv.FormatUint(uint64(h.Sum32()), 36) + "-" + strconv.Itoa(occurrence)
}

// HyperlinkIDFor returns the link id Hyperlink would derive for url.
// Callers that hand one link's text to HyperlinkID in several pieces use
// this to give every piece the same id.
func HyperlinkIDFor(url string) string { return hyperlinkIDFor(url, 0) }

// linkStateAfter replays the OSC 8 sequences in piece onto a prior state
// and returns the opening sequence in effect at the end of piece, or ""
// when no link is open. The OSC 8 counterpart of sgrStateAfter, and used
// by the same caller for the same reason: a wrapped row has to re-open
// what the previous row left open.
func linkStateAfter(state, piece string) string {
	for i := 0; i < len(piece); {
		n := escSeqLen(piece, i)
		if n == 0 {
			i++
			continue
		}
		seq := piece[i : i+n]
		if strings.HasPrefix(seq, "\x1b]8;") {
			if isHyperlinkClose(seq) {
				state = ""
			} else {
				state = seq
			}
		}
		i += n
	}
	return state
}

// isHyperlinkClose reports whether seq is an OSC 8 with an empty URI,
// which is the "end the current link" form. The params field between the
// two semicolons may carry an id even on a close, so the test is on the
// URI, not on the whole sequence being byte-equal to hyperlinkClose.
func isHyperlinkClose(seq string) bool {
	body := strings.TrimPrefix(seq, "\x1b]8;")
	body = strings.TrimSuffix(body, "\x1b\\")
	body = strings.TrimSuffix(body, "\x07")
	_, uri, ok := strings.Cut(body, ";")
	return ok && uri == ""
}
