// Package tuitest provides test doubles for driving the TUI without a
// real terminal: FakeTerm implements tui.Terminal with scripted byte
// input and captured output, and Screen replays that output through a
// VT-emulator (vt10x) so tests assert on the final cell grid a real
// terminal would display rather than on raw escape sequences.
//
// The two compose: NewFakeTerm embeds a Screen and tees every Write
// into it, so a test can type scripted keys, let the code under test
// render, and then assert Screen rows. Renderer-only tests can use a
// bare Screen as the io.Writer instead.
package tuitest

import (
	"io"
	"strings"
	"sync"
	"time"

	"github.com/hinshun/vt10x"

	"terva.sh/terva/packages/tui"
)

// FakeTerm is a tui.Terminal backed by a scripted input buffer and a
// VT-emulated screen. It is safe for the usual TUI topology: one
// goroutine reading input, one writing output, and the test goroutine
// scripting keys and asserting the screen.
type FakeTerm struct {
	mu        sync.Mutex
	cols      int
	rows      int
	resizeCBs []func()
	raw       strings.Builder
	screen    *Screen

	in     chan byte
	inOnce sync.Once
}

var _ tui.Terminal = (*FakeTerm)(nil)

// NewFakeTerm returns a FakeTerm of the given size with an empty
// screen and no pending input.
func NewFakeTerm(cols, rows int) *FakeTerm {
	return &FakeTerm{
		cols:   cols,
		rows:   rows,
		screen: NewScreen(cols, rows),
		// Large enough that tests never block feeding input; Type
		// panics on overflow rather than deadlocking the test.
		in: make(chan byte, 1<<14),
	}
}

// ---- tui.Terminal implementation ----

func (f *FakeTerm) Write(b []byte) (int, error) {
	f.mu.Lock()
	f.raw.Write(b)
	f.mu.Unlock()
	return f.screen.Write(b)
}

func (f *FakeTerm) Size() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cols, f.rows
}

func (f *FakeTerm) OnResize(fn func()) {
	f.mu.Lock()
	f.resizeCBs = append(f.resizeCBs, fn)
	f.mu.Unlock()
}

func (f *FakeTerm) EnterRaw() (func() error, error) {
	return func() error { return nil }, nil
}

// ReadByte blocks until a scripted byte is available; it returns
// io.EOF once CloseInput has been called and the buffer is drained,
// which is how a test shuts down the input-reader goroutine.
func (f *FakeTerm) ReadByte() (byte, error) {
	b, ok := <-f.in
	if !ok {
		return 0, io.EOF
	}
	return b, nil
}

func (f *FakeTerm) PeekByteTimeout(d time.Duration) (byte, bool, error) {
	select {
	case b, ok := <-f.in:
		if !ok {
			return 0, false, io.EOF
		}
		return b, true, nil
	case <-time.After(d):
		return 0, false, nil
	}
}

func (f *FakeTerm) SetNonblock(bool) error { return nil }

// ---- scripting API ----

// Type feeds bytes to the program under test as if the user typed
// them. Escape sequences are passed through verbatim, so callers can
// send special keys with their wire encoding (e.g. "\x1b[A" for Up).
func (f *FakeTerm) Type(s string) {
	for i := 0; i < len(s); i++ {
		select {
		case f.in <- s[i]:
		default:
			panic("tuitest: FakeTerm input buffer full")
		}
	}
}

// CloseInput ends the input script: pending bytes still drain, then
// ReadByte returns io.EOF. Safe to call more than once.
func (f *FakeTerm) CloseInput() {
	f.inOnce.Do(func() { close(f.in) })
}

// Resize changes the reported terminal size, resizes the emulated
// screen, and fires registered resize callbacks (like SIGWINCH).
func (f *FakeTerm) Resize(cols, rows int) {
	f.mu.Lock()
	f.cols, f.rows = cols, rows
	cbs := make([]func(), len(f.resizeCBs))
	copy(cbs, f.resizeCBs)
	f.mu.Unlock()
	f.screen.Resize(cols, rows)
	for _, cb := range cbs {
		cb()
	}
}

// Output returns everything written to the terminal so far, raw
// escape sequences included.
func (f *FakeTerm) Output() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.raw.String()
}

// Screen returns the VT-emulated view of everything written so far.
func (f *FakeTerm) Screen() *Screen { return f.screen }

// ---- Screen ----

// Screen is a VT-emulator-backed terminal grid. Feed it the byte
// stream a renderer produced (it is an io.Writer) and assert on the
// resulting rows — the same final state a real terminal would show.
type Screen struct {
	vt vt10x.Terminal
}

// NewScreen returns an empty emulated screen of the given size.
func NewScreen(cols, rows int) *Screen {
	return &Screen{vt: vt10x.New(vt10x.WithSize(cols, rows))}
}

func (s *Screen) Write(b []byte) (int, error) { return s.vt.Write(b) }

// Resize changes the emulated terminal size.
func (s *Screen) Resize(cols, rows int) {
	s.vt.Resize(cols, rows)
}

// Row returns the text of row y (0-based, top to bottom) with
// trailing whitespace trimmed and styling ignored.
func (s *Screen) Row(y int) string {
	s.vt.Lock()
	defer s.vt.Unlock()
	return s.rowLocked(y)
}

func (s *Screen) rowLocked(y int) string {
	cols, _ := s.vt.Size()
	var b strings.Builder
	for x := range cols {
		c := s.vt.Cell(x, y).Char
		if c == 0 {
			c = ' '
		}
		b.WriteRune(c)
	}
	return strings.TrimRight(b.String(), " ")
}

// Rows returns every row of the visible grid, top to bottom, each
// right-trimmed.
func (s *Screen) Rows() []string {
	s.vt.Lock()
	defer s.vt.Unlock()
	_, rows := s.vt.Size()
	out := make([]string, rows)
	for y := range rows {
		out[y] = s.rowLocked(y)
	}
	return out
}

// Text returns the visible grid as one string, rows joined with \n
// and trailing blank rows dropped. This is the natural shape for
// golden comparisons.
func (s *Screen) Text() string {
	rows := s.Rows()
	end := len(rows)
	for end > 0 && rows[end-1] == "" {
		end--
	}
	return strings.Join(rows[:end], "\n")
}

// Cursor returns the cursor position as (col, row), 0-based.
func (s *Screen) Cursor() (x, y int) {
	s.vt.Lock()
	defer s.vt.Unlock()
	c := s.vt.Cursor()
	return c.X, c.Y
}

// CursorVisible reports whether the cursor is currently shown.
func (s *Screen) CursorVisible() bool {
	s.vt.Lock()
	defer s.vt.Unlock()
	return s.vt.CursorVisible()
}
