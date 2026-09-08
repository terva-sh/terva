package tui

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/term"
)

// Terminal abstracts the real terminal for tests.
type Terminal interface {
	io.Writer
	// Size returns (cols, rows).
	Size() (int, int)
	// OnResize registers a callback invoked on SIGWINCH (best effort).
	//
	// This signature is load-bearing beyond terva. git-ticket's
	// tui.Terminal declares the identical method, which is why
	// *ProcTerm satisfies it with no adapter. Do not add a detach
	// return here; ProcTerm.OnResizeDetach carries that separately.
	OnResize(func())
	// EnterRaw puts the tty into raw mode. Returns a restore func.
	EnterRaw() (restore func() error, err error)
	// ReadByte reads one byte of input. Blocks.
	ReadByte() (byte, error)
	// PeekByteTimeout reads one byte of input but returns (0, false, nil)
	// if no byte arrives within the timeout. Used to disambiguate bare
	// Esc from the start of an escape sequence.
	PeekByteTimeout(time.Duration) (byte, bool, error)
	// SetNonblock sets stdin to non-blocking mode (used by paste handling).
	// May be a no-op on some platforms.
	SetNonblock(bool) error
}

// ProcTerm is a Terminal bound to the current process's tty.
type ProcTerm struct {
	out *os.File
	in  *os.File

	// resizeMu guards resizeCBs and resizeNextID. The signal goroutine
	// reads them while the main loop registers and detaches, so both
	// need the lock. tuitest.FakeTerm has always taken one here and
	// ProcTerm did not, which is why no test caught the race: the
	// double was correct and the real implementation was not.
	resizeMu     sync.Mutex
	resizeCBs    map[int]func()
	resizeNextID int

	// resizeOnce holds the SIGWINCH plumbing to one channel and one
	// goroutine however many callbacks register. installResizeHandler
	// used to run per registration, so N callbacks left N goroutines
	// each walking all N callbacks: N squared invocations per resize.
	resizeOnce sync.Once
}

// NewProcTerm returns a Terminal bound to stdin/stdout.
func NewProcTerm() *ProcTerm {
	return &ProcTerm{out: os.Stdout, in: os.Stdin}
}

func (p *ProcTerm) Write(b []byte) (int, error) { return p.out.Write(b) }

func (p *ProcTerm) Size() (int, int) {
	w, h, err := term.GetSize(int(p.out.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// OnResize registers a callback invoked on SIGWINCH (best effort). The
// callback stays for the life of the process. Use OnResizeDetach when
// the caller has to take it back.
func (p *ProcTerm) OnResize(fn func()) {
	p.OnResizeDetach(fn)
}

// OnResizeDetach registers a resize callback and returns a function
// that removes it. Detaching twice is safe, and a detached callback
// never fires again.
//
// It is a separate method rather than a new return on OnResize because
// that signature has to keep matching git-ticket's tui.Terminal. A
// caller holding the interface reaches this the way terva handles
// every other optional upgrade, with a type assertion:
//
//	if d, ok := term.(interface{ OnResizeDetach(func()) func() }); ok {
//		defer d.OnResizeDetach(repaint)()
//	}
func (p *ProcTerm) OnResizeDetach(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	p.resizeMu.Lock()
	if p.resizeCBs == nil {
		p.resizeCBs = make(map[int]func())
	}
	id := p.resizeNextID
	p.resizeNextID++
	p.resizeCBs[id] = fn
	p.resizeMu.Unlock()

	p.resizeOnce.Do(p.installResizeHandler)

	var once sync.Once
	return func() {
		once.Do(func() {
			p.resizeMu.Lock()
			delete(p.resizeCBs, id)
			p.resizeMu.Unlock()
		})
	}
}

// fireResize invokes every registered callback once. It snapshots under
// the lock and calls outside it, so a callback that registers or
// detaches another does not deadlock. tuitest.FakeTerm.Resize has the
// same shape.
func (p *ProcTerm) fireResize() {
	p.resizeMu.Lock()
	cbs := make([]func(), 0, len(p.resizeCBs))
	for _, cb := range p.resizeCBs {
		cbs = append(cbs, cb)
	}
	p.resizeMu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

func (p *ProcTerm) EnterRaw() (func() error, error) {
	fd := int(p.in.Fd())
	if !term.IsTerminal(fd) {
		return func() error { return nil }, nil
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() error { return term.Restore(fd, st) }, nil
}

func (p *ProcTerm) ReadByte() (byte, error) {
	var b [1]byte
	_, err := p.in.Read(b[:])
	return b[0], err
}

// PeekByteTimeout uses a platform-specific non-blocking read to decide
// whether another byte is available within d. If not, returns (0, false, nil).
func (p *ProcTerm) PeekByteTimeout(d time.Duration) (byte, bool, error) {
	return peekStdin(p.in, d)
}

// ---- terminal control sequences ----

// HideCursor, ShowCursor, ClearScreen, BracketedPasteOn/Off, etc.
const (
	SeqHideCursor  = "\x1b[?25l"
	SeqShowCursor  = "\x1b[?25h"
	SeqClearScreen = "\x1b[2J\x1b[H"
	// SeqClearScreenNoHome is SeqClearScreen without the trailing
	// cursor-home (\x1b[H). Use it whenever a MoveTo(...) follows
	// immediately. VS Code's integrated terminal interprets a bare
	// CUP-no-args during a clear as "snap the viewport scrollbar to
	// row 0", which makes the user's scroll position jump on every
	// repaint. The explicit MoveTo we emit afterwards positions the
	// cursor identically without triggering that snap.
	SeqClearScreenNoHome = "\x1b[2J"
	SeqClearScrollback   = "\x1b[3J"
	// SeqCursorHome moves to the top-left of the visible viewport.
	// SeqClearToEnd erases from the cursor to the end of the screen
	// without scrolling content into scrollback (unlike \x1b[2J on
	// xterm.js). Together they clear the visible frame in place, which
	// the VS Code terminal needs for duplicate-free full repaints.
	SeqCursorHome        = "\x1b[H"
	SeqClearToEnd        = "\x1b[0J"
	SeqClearLine         = "\x1b[2K"
	SeqResetScrollRegion = "\x1b[r"
	SeqDeleteKittyImages = "\x1b_Ga=d\x1b\\"
	SeqBracketedPasteOn  = "\x1b[?2004h"
	SeqBracketedPasteOff = "\x1b[?2004l"
	// Request enhanced keyboard reporting where supported. The kitty
	// keyboard protocol covers Ghostty, Kitty, VS Code's integrated
	// terminal, and recent xterm.js builds; xterm modifyOtherKeys is a
	// useful fallback for terminals/tmux that expose a modified Enter as
	// CSI 27;<mod>;<code>~. Lets us tell Shift+Enter from a bare Enter.
	SeqEnhancedKeyboardOn  = "\x1b[>1u\x1b[>4;2m"
	SeqEnhancedKeyboardOff = "\x1b[<u\x1b[>4m"
	// Basic mouse tracking + SGR extended coordinates. Used only
	// when explicitly enabled by the interactive mode (currently VS
	// Code terminal) so terminals with good native scrolling, like
	// Ghostty, are left alone.
	SeqMouseOn         = "\x1b[?1000h\x1b[?1006h"
	SeqMouseOff        = "\x1b[?1000l\x1b[?1006l"
	SeqAltScreenOn     = "\x1b[?1049h"
	SeqAltScreenOff    = "\x1b[?1049l"
	SeqSynchronizedOn  = "\x1b[?2026h"
	SeqSynchronizedOff = "\x1b[?2026l"
	// SeqSaveCursor / SeqRestoreCursor use DECSC/DECRC. All terminals
	// we target adjust the saved row when natural scrolling occurs, so
	// these survive scroll-on-write inside the bottom band.
	SeqSaveCursor    = "\x1b7"
	SeqRestoreCursor = "\x1b8"
	// SeqEraseToEnd erases from the cursor to the end of the screen.
	SeqEraseToEnd = "\x1b[J"
)

// ReportCWD returns the OSC 7 sequence that tells the terminal the
// current working directory, formatted as a file URL. Terminals such
// as kitty, WezTerm, iTerm2, and GNOME Terminal use this to open new
// tabs / splits in the same directory instead of inheriting a stale
// directory from an extension subprocess. Returns "" for an empty path
// so callers can write it unconditionally. The path is percent-encoded
// per RFC 3986 (only unreserved characters and the path separator are
// left bare) and prefixed with the local hostname.
func ReportCWD(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	host, _ := os.Hostname()
	return "\x1b]7;file://" + host + osc7EscapePath(abs) + "\x07"
}

// osc7EscapePath percent-encodes a filesystem path for an OSC 7 file
// URL. Forward slashes (the URL path separator) and the unreserved set
// (A-Z a-z 0-9 - _ . ~) pass through; everything else is %XX-encoded.
// On Windows the backslash separators are normalised to forward slashes
// and a leading slash is added so "C:\foo" becomes "/C:/foo".
func osc7EscapePath(p string) string {
	p = filepath.ToSlash(p)
	if len(p) > 1 && p[1] == ':' {
		p = "/" + p
	}
	const hex = "0123456789ABCDEF"
	var b []byte
	for i := 0; i < len(p); i++ {
		c := p[i]
		unreserved := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' || c == '/'
		if unreserved {
			b = append(b, c)
			continue
		}
		b = append(b, '%', hex[c>>4], hex[c&0x0f])
	}
	return string(b)
}

// MoveTo moves the cursor to 1-indexed (row, col).
func MoveTo(row, col int) string {
	return "\x1b[" + itoa(row) + ";" + itoa(col) + "H"
}
