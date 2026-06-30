package modes

import (
	"testing"
	"time"
)

// TestBusyIntervalForFPS checks the fps -> interval mapping, including
// the idle-floor clamp that makes high/zero fps mean "uncapped".
func TestBusyIntervalForFPS(t *testing.T) {
	cases := []struct {
		fps  int
		want time.Duration
	}{
		{30, time.Second / 30},    // ~33ms, the default cap
		{20, time.Second / 20},    // 50ms
		{60, time.Second / 60},    // ~16.6ms, just above the floor (not clamped)
		{0, idleRedrawInterval},   // uncapped
		{120, idleRedrawInterval}, // ~8ms < floor -> clamped to floor (uncapped)
		{-5, idleRedrawInterval},  // nonsense -> uncapped
	}
	for _, c := range cases {
		if got := busyIntervalForFPS(c.fps); got != c.want {
			t.Errorf("busyIntervalForFPS(%d) = %v, want %v", c.fps, got, c.want)
		}
	}
}

// TestResolveBusyRedrawInterval covers env override parsing and the note
// emitted for a bug report. defaultRedrawFPS is 30 in non-pprof test
// builds.
func TestResolveBusyRedrawInterval(t *testing.T) {
	// Derive the expected default from the build's defaultRedrawFPS so this
	// stays correct under both the normal (30) and terva_pprof (0) builds.
	wantDefault := busyIntervalForFPS(defaultRedrawFPS)
	t.Run("default (no env)", func(t *testing.T) {
		t.Setenv("TERVA_REDRAW_FPS", "")
		got, note := resolveBusyRedrawInterval()
		if got != wantDefault {
			t.Errorf("default interval = %v, want %v", got, wantDefault)
		}
		if note != "" {
			t.Errorf("default should not log a note, got %q", note)
		}
	})
	t.Run("override raises fps", func(t *testing.T) {
		t.Setenv("TERVA_REDRAW_FPS", "60")
		got, note := resolveBusyRedrawInterval()
		if got != time.Second/60 {
			t.Errorf("60fps interval = %v, want %v", got, time.Second/60)
		}
		if note == "" {
			t.Error("override should log a note for bug reports")
		}
	})
	t.Run("zero is uncapped + noted", func(t *testing.T) {
		t.Setenv("TERVA_REDRAW_FPS", "0")
		got, note := resolveBusyRedrawInterval()
		if got != idleRedrawInterval {
			t.Errorf("0fps interval = %v, want %v", got, idleRedrawInterval)
		}
		if note == "" {
			t.Error("uncapped override should log a note")
		}
	})
	t.Run("invalid falls back + notes", func(t *testing.T) {
		t.Setenv("TERVA_REDRAW_FPS", "banana")
		got, note := resolveBusyRedrawInterval()
		if got != wantDefault {
			t.Errorf("invalid interval = %v, want default %v", got, wantDefault)
		}
		if note == "" {
			t.Error("invalid value should log a note")
		}
	})
}
