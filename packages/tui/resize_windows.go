//go:build windows

package tui

import (
	"bufio"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// VT byte input hides console resize records, so poll the output size.
func (p *ProcTerm) installResizeHandler() {
	p.startResizePolling(p.Size, 100*time.Millisecond)
}

func (p *ProcTerm) resumeResizeHandler() {
	p.resizeMu.Lock()
	hasCallbacks := len(p.resizeCBs) > 0
	p.resizeMu.Unlock()
	if hasCallbacks {
		p.installResizeHandler()
	}
}

func (p *ProcTerm) stopResizeHandler() { p.stopResizePolling() }

func (p *ProcTerm) SetNonblock(enable bool) error { return nil }

var (
	consoleKernel32  = windows.NewLazySystemDLL("kernel32.dll")
	peekConsoleInput = consoleKernel32.NewProc("PeekConsoleInputW")
	readConsoleInput = consoleKernel32.NewProc("ReadConsoleInputW")
	peekNamedPipe    = consoleKernel32.NewProc("PeekNamedPipe")
)

func consoleHasByte(h windows.Handle) (bool, error) {
	// Cap each probe so event floods cannot outrun the input deadline.
	var records [128]consoleInputRecord
	var count uint32
	if r, _, err := peekConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&count))); r == 0 {
		return false, err
	}
	for j := uint32(0); j < count; j++ {
		if records[j].yieldsByte() {
			return true, nil
		}
	}
	// Key-up, focus and mouse records signal the handle but yield no bytes.
	if count > 0 {
		var read uint32
		if r, _, err := readConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&records[0])), uintptr(count), uintptr(unsafe.Pointer(&read))); r == 0 {
			return false, err
		}
	}
	return false, nil
}

func pipeHasByte(h windows.Handle) (bool, error) {
	var available uint32
	if r, _, err := peekNamedPipe.Call(uintptr(h), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0); r == 0 {
		if err == windows.ERROR_BROKEN_PIPE {
			return false, io.EOF
		}
		return false, err
	}
	return available > 0, nil
}

func (p *ProcTerm) stdinReader() *bufio.Reader {
	if p.inputReader == nil {
		// Go 1.27 caps console reads at 10,000 UTF-16 units. Allow room for UTF-8 expansion.
		p.inputReader = bufio.NewReaderSize(p.in, 64*1024)
	}
	return p.inputReader
}

func readProcStdin(p *ProcTerm) (byte, error) { return p.stdinReader().ReadByte() }

func peekProcStdin(p *ProcTerm, d time.Duration) (byte, bool, error) {
	return peekBufferedInput(p.stdinReader(), func() (bool, error) { return waitStdin(p.in, d) })
}

func peekStdin(in *os.File, d time.Duration) (byte, bool, error) {
	ready, err := waitStdin(in, d)
	if !ready || err != nil {
		return 0, false, err
	}
	var b [1]byte
	_, err = in.Read(b[:])
	return b[0], err == nil, err
}

func waitStdin(in *os.File, d time.Duration) (bool, error) {
	h := windows.Handle(in.Fd())
	var mode uint32
	isConsole := windows.GetConsoleMode(h, &mode) == nil
	if !isConsole {
		kind, err := windows.GetFileType(h)
		if err != nil {
			return false, err
		}
		if kind != windows.FILE_TYPE_PIPE {
			return true, nil
		}
	}
	deadline := time.Now().Add(d)
	for {
		var ready bool
		var err error
		if isConsole {
			ready, err = consoleHasByte(h)
		} else {
			ready, err = pipeHasByte(h)
		}
		if err != nil {
			return false, err
		}
		if ready {
			return true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		time.Sleep(min(remaining, time.Millisecond))
	}
}
