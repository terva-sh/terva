package tui

import "encoding/binary"

// consoleInputRecord matches Win32 INPUT_RECORD, including its union alignment.
type consoleInputRecord struct {
	eventType uint16
	_         uint16
	event     [16]byte
}

func (r consoleInputRecord) yieldsByte() bool {
	if r.eventType != 1 || binary.LittleEndian.Uint32(r.event[:4]) == 0 {
		return false
	}
	if binary.LittleEndian.Uint16(r.event[10:12]) != 0 {
		return true
	}
	key := binary.LittleEndian.Uint16(r.event[6:8])
	// Ctrl+Space and Ctrl+2 encode NUL in VT input mode.
	if binary.LittleEndian.Uint32(r.event[12:16])&0x0c != 0 && (key == 0x20 || key == 0x32) {
		return true
	}
	// VT mode translates navigation and function keys into escape sequences.
	return key >= 0x21 && key <= 0x28 || key == 0x2d || key == 0x2e || key >= 0x70 && key <= 0x87
}
