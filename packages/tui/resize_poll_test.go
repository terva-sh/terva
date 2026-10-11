package tui

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestResizePollingNotifiesStopsAndRestarts(t *testing.T) {
	p := &ProcTerm{resizeCBs: map[int]func(){}}
	fired := make(chan struct{}, 8)
	p.resizeCBs[0] = func() { fired <- struct{}{} }
	var width atomic.Int32
	width.Store(80)
	size := func() (int, int) { return int(width.Load()), 24 }
	p.startResizePolling(size, time.Millisecond)
	p.startResizePolling(size, time.Millisecond)
	width.Store(100)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("idle resize did not notify")
	}
	done := p.stopResizePolling()
	p.stopResizePolling()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("resize monitor did not stop")
	}
	width.Store(120)
	select {
	case <-fired:
		t.Fatal("monitor fired after stopping")
	case <-time.After(10 * time.Millisecond):
	}
	p.startResizePolling(size, time.Millisecond)
	width.Store(140)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("resize monitor did not restart")
	}
	<-p.stopResizePolling()
}

func TestResizePollingTeardownWaitsForSizeProbeAndRestart(t *testing.T) {
	p := &ProcTerm{resizeCBs: map[int]func(){}}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var probes atomic.Int32
	p.startResizePolling(func() (int, int) {
		if probes.Add(1) == 2 {
			close(entered)
			<-release
		}
		return 80, 24
	}, time.Millisecond)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("size probe did not start")
	}
	p.resizeMu.Lock()
	stop := p.resizeStop
	p.resizeMu.Unlock()
	stopped := make(chan struct{})
	go func() { p.stopResizePolling(); close(stopped) }()
	select {
	case <-stop:
	case <-time.After(time.Second):
		t.Fatal("teardown did not request cancellation")
	}
	started := make(chan struct{})
	go func() {
		p.startResizePolling(func() (int, int) { return 100, 24 }, time.Hour)
		close(started)
	}()
	select {
	case <-stopped:
		t.Fatal("teardown returned while the size probe was active")
	case <-started:
		t.Fatal("restart returned while the old monitor was active")
	case <-time.After(10 * time.Millisecond):
	}
	unblock()
	for _, ch := range []<-chan struct{}{stopped, started} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("monitor lifecycle did not complete")
		}
	}
	if done := p.stopResizePolling(); done == nil {
		t.Fatal("restart did not install a monitor")
	}
}

func TestResizePollingCallbackCanStopAndRestart(t *testing.T) {
	p := &ProcTerm{resizeCBs: map[int]func(){}}
	p.resizeOnce.Do(func() {})
	completed := make(chan struct{})
	p.resizeCBs[0] = func() {
		detach := p.OnResizeDetach(func() {})
		detach()
		p.stopResizePolling()
		p.startResizePolling(func() (int, int) { return 100, 24 }, time.Hour)
		close(completed)
	}
	var width atomic.Int32
	width.Store(80)
	p.startResizePolling(func() (int, int) { return int(width.Load()), 24 }, time.Millisecond)
	width.Store(100)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("callback deadlocked during lifecycle reentry")
	}
	p.stopResizePolling()
}

func TestConsoleInputRecordsProduceOnlyReadableKeys(t *testing.T) {
	if unsafe.Sizeof(consoleInputRecord{}) != 20 {
		t.Fatal("incorrect Win32 record size")
	}
	for _, tc := range []struct {
		name      string
		eventType uint16
		down      bool
		char, key uint16
		want      bool
	}{
		{"text", 1, true, 'x', 0, true},
		{"key up", 1, false, 'x', 0, false},
		{"resize", 4, true, 'x', 0, false},
		{"focus", 16, true, 'x', 0, false},
		{"modifier", 1, true, 0, 0x10, false},
		{"arrow", 1, true, 0, 0x25, true},
		{"function", 1, true, 0, 0x70, true},
		{"escape", 1, true, 27, 0x1b, true},
	} {
		r := consoleInputRecord{eventType: tc.eventType}
		if tc.down {
			binary.LittleEndian.PutUint32(r.event[:4], 1)
		}
		binary.LittleEndian.PutUint16(r.event[4:6], 1)
		binary.LittleEndian.PutUint16(r.event[6:8], tc.key)
		binary.LittleEndian.PutUint16(r.event[10:12], tc.char)
		if got := r.yieldsByte(); got != tc.want {
			t.Errorf("%s: yieldsByte=%v", tc.name, got)
		}
	}
}

func TestConsoleNavigationUsesVirtualKeyNotRepeatCount(t *testing.T) {
	// KEY_EVENT_RECORD stores repeat count at 4 and virtual-key code at 6.
	for _, tc := range []struct {
		name  string
		event [16]byte
		want  bool
	}{
		{"left arrow", [16]byte{1, 0, 0, 0, 1, 0, 0x25}, true},
		{"F1", [16]byte{1, 0, 0, 0, 1, 0, 0x70}, true},
		{"shift repeated 37 times", [16]byte{1, 0, 0, 0, 0x25, 0, 0x10}, false},
	} {
		if got := (consoleInputRecord{eventType: 1, event: tc.event}).yieldsByte(); got != tc.want {
			t.Errorf("%s: yieldsByte=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestConsoleNULChordsRemainReadable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event [16]byte
		want  bool
	}{
		{"left Ctrl+Space", [16]byte{0: 1, 4: 1, 6: 0x20, 12: 0x08}, true},
		{"right Ctrl+Space", [16]byte{0: 1, 4: 1, 6: 0x20, 12: 0x04}, true},
		{"Ctrl+@", [16]byte{0: 1, 4: 1, 6: 0x32, 12: 0x14}, true},
		{"space without Ctrl", [16]byte{0: 1, 4: 1, 6: 0x20}, false},
		{"Ctrl alone", [16]byte{0: 1, 4: 1, 6: 0x11, 12: 0x08}, false},
	} {
		if got := (consoleInputRecord{eventType: 1, event: tc.event}).yieldsByte(); got != tc.want {
			t.Errorf("%s: yieldsByte=%v, want %v", tc.name, got, tc.want)
		}
	}
}
