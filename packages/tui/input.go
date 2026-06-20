package tui

import (
	"strconv"
	"strings"
	"time"
)

// Key is a parsed keypress.
type Key struct {
	Kind  KeyKind
	Rune  rune   // for KeyRune
	Paste string // for KeyPaste
	Ctrl  bool
	Alt   bool
	Shift bool
}

type KeyKind int

const (
	KeyRune KeyKind = iota
	KeyEnter
	KeyBackspace
	KeyTab
	KeyShiftTab
	KeyEsc
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyDelete
	KeyCtrlC
	KeyCtrlD
	KeyCtrlL
	KeyCtrlU
	KeyCtrlK
	KeyCtrlA
	KeyCtrlE
	KeyCtrlW
	KeyCtrlO
	KeyPaste
	KeyMouseWheelUp
	KeyMouseWheelDown
	KeyUnknown
)

// Reader parses a byte stream into Key events. It understands basic
// xterm escape sequences and bracketed paste.
type Reader struct {
	src  func() (byte, error)
	peek func(time.Duration) (byte, bool, error) // optional; may be nil
}

// NewReader returns a Reader that pulls bytes from read.
func NewReader(read func() (byte, error)) *Reader { return &Reader{src: read} }

// NewReaderWithPeek returns a Reader that pulls bytes from read and uses
// peek to disambiguate bare Esc from the start of an escape sequence.
func NewReaderWithPeek(read func() (byte, error), peek func(time.Duration) (byte, bool, error)) *Reader {
	return &Reader{src: read, peek: peek}
}

// Read returns the next parsed Key.
func (r *Reader) Read() (Key, error) {
	b, err := r.src()
	if err != nil {
		return Key{}, err
	}
	switch {
	case b == 0x03:
		return Key{Kind: KeyCtrlC}, nil
	case b == 0x04:
		return Key{Kind: KeyCtrlD}, nil
	case b == 0x0c:
		return Key{Kind: KeyCtrlL}, nil
	case b == 0x15:
		return Key{Kind: KeyCtrlU}, nil
	case b == 0x0b:
		return Key{Kind: KeyCtrlK}, nil
	case b == 0x01:
		return Key{Kind: KeyCtrlA}, nil
	case b == 0x05:
		return Key{Kind: KeyCtrlE}, nil
	case b == 0x17:
		return Key{Kind: KeyCtrlW}, nil
	case b == 0x0f:
		return Key{Kind: KeyCtrlO}, nil
	case b == '\r', b == '\n':
		return Key{Kind: KeyEnter}, nil
	case b == '\t':
		return Key{Kind: KeyTab}, nil
	case b == 0x7f, b == 0x08:
		return Key{Kind: KeyBackspace}, nil
	case b == 0x1b:
		return r.readEscape()
	case b < 0x20:
		return Key{Kind: KeyUnknown}, nil
	}
	// UTF-8 multibyte?
	if b < 0x80 {
		return Key{Kind: KeyRune, Rune: rune(b)}, nil
	}
	// Decode UTF-8 (up to 4 bytes).
	n := utf8Len(b)
	buf := []byte{b}
	for i := 1; i < n; i++ {
		bb, err := r.src()
		if err != nil {
			return Key{}, err
		}
		buf = append(buf, bb)
	}
	rn, _ := decodeRune(buf)
	return Key{Kind: KeyRune, Rune: rn}, nil
}

func utf8Len(b byte) int {
	switch {
	case b&0xe0 == 0xc0:
		return 2
	case b&0xf0 == 0xe0:
		return 3
	case b&0xf8 == 0xf0:
		return 4
	}
	return 1
}

func decodeRune(b []byte) (rune, int) {
	// Minimal decoder; invalid runes become U+FFFD.
	if len(b) == 1 {
		return rune(b[0]), 1
	}
	var r rune
	switch len(b) {
	case 2:
		r = rune(b[0]&0x1f)<<6 | rune(b[1]&0x3f)
	case 3:
		r = rune(b[0]&0x0f)<<12 | rune(b[1]&0x3f)<<6 | rune(b[2]&0x3f)
	case 4:
		r = rune(b[0]&0x07)<<18 | rune(b[1]&0x3f)<<12 | rune(b[2]&0x3f)<<6 | rune(b[3]&0x3f)
	default:
		r = 0xFFFD
	}
	return r, len(b)
}

// readEscape handles sequences starting with 0x1b.
func (r *Reader) readEscape() (Key, error) {
	// Bare ESC: maybe followed by another byte within a short window.
	b, have, err := r.readEscapeNext(50 * time.Millisecond)
	if err != nil || !have {
		return Key{Kind: KeyEsc}, nil
	}
	switch b {
	case '[':
		return r.readCSI()
	case 'O':
		// SS3 sequences (function keys in some terminals).
		c, err := r.src()
		if err != nil {
			return Key{}, err
		}
		switch c {
		case 'H':
			return Key{Kind: KeyHome}, nil
		case 'F':
			return Key{Kind: KeyEnd}, nil
		}
		return Key{Kind: KeyUnknown}, nil
	case 0x7f, 0x08:
		// Alt+Backspace (Option+Delete on macOS) — most terminals send
		// ESC + DEL for this. Surface as a dedicated "alt backspace"
		// so the editor can map it to delete-word.
		return Key{Kind: KeyBackspace, Alt: true}, nil
	case 'b':
		// Emacs-style word-left, also emitted by some terminals for
		// Option+LeftArrow.
		return Key{Kind: KeyLeft, Alt: true}, nil
	case 'f':
		// Emacs-style word-right, also emitted for Option+RightArrow.
		return Key{Kind: KeyRight, Alt: true}, nil
	default:
		// Alt+<char>
		if b < 0x80 {
			return Key{Kind: KeyRune, Rune: rune(b), Alt: true}, nil
		}
	}
	return Key{Kind: KeyUnknown}, nil
}

// readEscapeNext tries to read one byte within d. If peek is available
// we use it (true non-blocking). Otherwise we fall back to a blocking
// read, which means bare Esc is only detected after the next keystroke.
func (r *Reader) readEscapeNext(d time.Duration) (byte, bool, error) {
	if r.peek != nil {
		return r.peek(d)
	}
	b, err := r.src()
	if err != nil {
		return 0, false, err
	}
	return b, true, nil
}

// readCSI parses a CSI sequence after ESC [.
func (r *Reader) readCSI() (Key, error) {
	var params []byte
	for {
		c, err := r.src()
		if err != nil {
			return Key{}, err
		}
		if c >= 0x30 && c <= 0x3f {
			params = append(params, c)
			continue
		}
		// Final byte.
		return r.dispatchCSI(string(params), c), nil
	}
}

func (r *Reader) dispatchCSI(params string, final byte) Key {
	// SGR mouse mode: CSI < button ; x ; y M/m. Wheel events use
	// button codes 64 (up) and 65 (down). We ignore coordinates for
	// now; the chat view only needs scroll direction.
	if strings.HasPrefix(params, "<") && (final == 'M' || final == 'm') {
		parts := strings.Split(strings.TrimPrefix(params, "<"), ";")
		if len(parts) >= 1 {
			switch parts[0] {
			case "64":
				return Key{Kind: KeyMouseWheelUp}
			case "65":
				return Key{Kind: KeyMouseWheelDown}
			}
		}
		return Key{Kind: KeyUnknown}
	}

	// Modified keys arrive as CSI 1;<mod><final> (arrows), CSI <code>;<mod>u
	// (kitty keyboard protocol), or CSI 27;<mod>;<code>~ (xterm
	// modifyOtherKeys). Decode the modifier bitmask, then route the
	// protocol-specific encodings before the bare-final arrow switch.
	shift, alt := parseCSIModifiers(params)
	if final == 'u' {
		if key, ok := parseCSIU(params); ok {
			return key
		}
	}
	if final == '~' {
		if key, ok := parseModifyOtherKeys(params); ok {
			return key
		}
	}
	switch final {
	case 'A':
		return Key{Kind: KeyUp, Alt: alt, Shift: shift}
	case 'B':
		return Key{Kind: KeyDown, Alt: alt, Shift: shift}
	case 'C':
		return Key{Kind: KeyRight, Alt: alt, Shift: shift}
	case 'D':
		return Key{Kind: KeyLeft, Alt: alt, Shift: shift}
	case 'H':
		return Key{Kind: KeyHome}
	case 'F':
		return Key{Kind: KeyEnd}
	case 'Z':
		return Key{Kind: KeyShiftTab}
	case '~':
		switch params {
		case "3":
			return Key{Kind: KeyDelete}
		case "5":
			return Key{Kind: KeyPageUp}
		case "6":
			return Key{Kind: KeyPageDown}
		case "200":
			// Start of bracketed paste.
			return r.readPaste()
		}
	}
	return Key{Kind: KeyUnknown}
}

func parseCSIModifiers(params string) (shift, alt bool) {
	if params == "" {
		return false, false
	}
	i := strings.LastIndexByte(params, ';')
	if i < 0 || i+1 >= len(params) {
		return false, false
	}
	mod, err := strconv.Atoi(params[i+1:])
	if err != nil {
		return false, false
	}
	// Xterm-style modifier values are 1 plus a bitmask:
	// 2=Shift, 3=Alt, 4=Shift+Alt, 5=Ctrl, 6=Shift+Ctrl,
	// 7=Alt+Ctrl, 8=Shift+Alt+Ctrl.
	bits := mod - 1
	return bits&1 != 0, bits&2 != 0
}

// parseCSIU decodes the kitty keyboard protocol's CSI <code>;<mod>u form.
func parseCSIU(params string) (Key, bool) {
	parts := strings.Split(params, ";")
	if len(parts) == 0 {
		return Key{}, false
	}
	code, err := strconv.Atoi(parts[0])
	if err != nil {
		return Key{}, false
	}
	mod := 1
	if len(parts) >= 2 {
		if mod, err = strconv.Atoi(parts[1]); err != nil {
			return Key{}, false
		}
	}
	return keyFromModifiedCode(code, mod)
}

// parseModifyOtherKeys decodes xterm's CSI 27;<mod>;<code>~ form.
func parseModifyOtherKeys(params string) (Key, bool) {
	parts := strings.Split(params, ";")
	if len(parts) != 3 || parts[0] != "27" {
		return Key{}, false
	}
	mod, err := strconv.Atoi(parts[1])
	if err != nil {
		return Key{}, false
	}
	code, err := strconv.Atoi(parts[2])
	if err != nil {
		return Key{}, false
	}
	return keyFromModifiedCode(code, mod)
}

func keyFromModifiedCode(code, mod int) (Key, bool) {
	bits := mod - 1
	shift := bits&1 != 0
	alt := bits&2 != 0
	ctrl := bits&4 != 0
	// Kitty keyboard protocol (CSI ... u) reports control keys as their
	// codepoints: Esc=27, Enter=13, Tab=9, Backspace=127. With enhanced
	// mode enabled they come through here, so map them back to their
	// dedicated keys (otherwise Esc would stop aborting the agent).
	switch code {
	case 13:
		return Key{Kind: KeyEnter, Shift: shift, Alt: alt, Ctrl: ctrl}, true
	case 27:
		return Key{Kind: KeyEsc, Shift: shift, Alt: alt, Ctrl: ctrl}, true
	case 9:
		if shift {
			return Key{Kind: KeyShiftTab, Alt: alt, Ctrl: ctrl}, true
		}
		return Key{Kind: KeyTab, Shift: shift, Alt: alt, Ctrl: ctrl}, true
	case 127, 8:
		return Key{Kind: KeyBackspace, Shift: shift, Alt: alt, Ctrl: ctrl}, true
	}
	if ctrl {
		switch code {
		case 'c', 'C':
			return Key{Kind: KeyCtrlC, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'd', 'D':
			return Key{Kind: KeyCtrlD, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'l', 'L':
			return Key{Kind: KeyCtrlL, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'u', 'U':
			return Key{Kind: KeyCtrlU, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'k', 'K':
			return Key{Kind: KeyCtrlK, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'a', 'A':
			return Key{Kind: KeyCtrlA, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'e', 'E':
			return Key{Kind: KeyCtrlE, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'w', 'W':
			return Key{Kind: KeyCtrlW, Shift: shift, Alt: alt, Ctrl: true}, true
		case 'o', 'O':
			return Key{Kind: KeyCtrlO, Shift: shift, Alt: alt, Ctrl: true}, true
		}
	}
	return Key{}, false
}

// readPaste reads until ESC [ 2 0 1 ~ and returns the pasted text.
// maxPasteBytes caps how much of a bracketed paste is kept. Beyond
// the cap the stream is still drained to the end marker (otherwise
// the remainder would spill into the editor as garbage key events)
// but discarded, and a visible truncation note is appended so the
// user knows their data was cut rather than silently submitting an
// incomplete paste. A var, not a const, so tests can lower it.
var maxPasteBytes = 2 << 20 // 2 MiB

func (r *Reader) readPaste() Key {
	var sb strings.Builder
	const end = "\x1b[201~"
	truncated := false
	tail := make([]byte, 0, len(end))
	for {
		b, err := r.src()
		if err != nil {
			break
		}
		tail = append(tail, b)
		if len(tail) > len(end) {
			if sb.Len() < maxPasteBytes {
				sb.WriteByte(tail[0])
			} else {
				truncated = true
			}
			tail = tail[1:]
		}
		if string(tail) == end {
			break
		}
	}
	paste := sb.String()
	if truncated {
		paste += "\n[paste truncated: exceeded the size limit]"
	}
	return Key{Kind: KeyPaste, Paste: paste}
}
