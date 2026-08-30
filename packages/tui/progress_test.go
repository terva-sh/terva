package tui

import (
	"strings"
	"testing"
)

// withProgress flips the process-wide gate for one test and puts the
// previous value back. The gate is global state shared by every test in
// this binary, so a test that set it and walked away would decide the
// behaviour of the ones that follow.
func withProgress(t *testing.T, on bool) {
	t.Helper()
	prev := ProgressEnabled()
	SetProgress(on)
	t.Cleanup(func() { SetProgress(prev) })
}

// neutralTerminalEnv erases every variable detectProgressSupportAuto
// consults, leaving a plain, unremarkable terminal. Without this the
// detection tests would assert the machine they ran on: the suite passes
// in a bare CI shell and fails on the developer's WezTerm, or vice
// versa.
func neutralTerminalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	for _, k := range []string{
		"TERVA_PROGRESS", "ZOT_PROGRESS", // rename:keep — envcompat reads both spellings
		"TMUX", "STY",
		"ConEmuANSI", "ConEmuPID",
		"WT_SESSION", "WEZTERM_PANE", "TERM_PROGRAM",
	} {
		t.Setenv(k, "")
	}
}

// The gate is what stands between a user on an unknown terminal and a
// stray escape sequence, so "off means silent" is the property to pin.
func TestProgressEmitsNothingWhenDisabled(t *testing.T) {
	withProgress(t, false)
	if got := ProgressBusy(); got != "" {
		t.Errorf("ProgressBusy() with the gate off = %q, want \"\"", got)
	}
	if got := ProgressIdle(); got != "" {
		t.Errorf("ProgressIdle() with the gate off = %q, want \"\"", got)
	}
}

// The exact bytes matter to somebody who is not a terminal: tooling greps
// a recording for these, so a change to the payload is a breaking change
// to that contract and should have to edit this test to happen.
func TestProgressSequencesAreWellFormedOSC94(t *testing.T) {
	withProgress(t, true)

	if got, want := ProgressBusy(), "\x1b]9;4;3;0\x1b\\"; got != want {
		t.Errorf("ProgressBusy() = %q, want %q (state 3 = indeterminate)", got, want)
	}
	if got, want := ProgressIdle(), "\x1b]9;4;0;0\x1b\\"; got != want {
		t.Errorf("ProgressIdle() = %q, want %q (state 0 = remove)", got, want)
	}

	// Both must be a single, properly terminated OSC. An unterminated
	// sequence would swallow whatever terva painted next.
	for name, seq := range map[string]string{"busy": ProgressBusy(), "idle": ProgressIdle()} {
		if !strings.HasPrefix(seq, "\x1b]9;4;") {
			t.Errorf("%s: %q does not open with OSC 9;4", name, seq)
		}
		if !strings.HasSuffix(seq, "\x1b\\") {
			t.Errorf("%s: %q is not ST-terminated", name, seq)
		}
		if n := escSeqLen(seq, 0); n != len(seq) {
			t.Errorf("%s: escSeqLen = %d, want %d — %q is not exactly one escape sequence", name, n, len(seq), seq)
		}
	}
}

// The override is the documented way to force the signal into a
// recording on a terminal the allowlist does not cover, so it has to beat
// the sniff in both directions.
func TestDetectProgressSupportEnvOverrideBeatsTheSniff(t *testing.T) {
	t.Run("on forces a terminal the sniff would reject", func(t *testing.T) {
		neutralTerminalEnv(t)
		t.Setenv("TERVA_PROGRESS", "on")
		if !DetectProgressSupport() {
			t.Error("TERVA_PROGRESS=on did not enable progress")
		}
	})
	t.Run("off forces a terminal the sniff would accept", func(t *testing.T) {
		neutralTerminalEnv(t)
		t.Setenv("WT_SESSION", "abc") // sniff would say yes
		t.Setenv("TERVA_PROGRESS", "off")
		if DetectProgressSupport() {
			t.Error("TERVA_PROGRESS=off did not disable progress on an allowlisted terminal")
		}
	})
}

func TestDetectProgressSupportAllowlist(t *testing.T) {
	// Only terminals known to implement OSC 9;4. Each is keyed off the
	// variable that terminal actually sets.
	for name, env := range map[string]map[string]string{
		"Windows Terminal":  {"WT_SESSION": "0e7a-..."},
		"ConEmu":            {"ConEmuPID": "1234"},
		"WezTerm (pane)":    {"WEZTERM_PANE": "0"},
		"WezTerm (program)": {"TERM_PROGRAM": "WezTerm"},
	} {
		t.Run(name, func(t *testing.T) {
			neutralTerminalEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if !DetectProgressSupport() {
				t.Errorf("%s should be detected as supporting OSC 9;4", name)
			}
		})
	}
}

// The reason the allowlist is short. A bare OSC 9 is iTerm2's "post a
// desktop notification" extension; a terminal that implements that but
// not the ";4" progress sub-parameter would show the user a toast reading
// "4;3;0" on every single turn. Silence is the only safe default there,
// and these two are the terminals most likely to be recorded on.
func TestDetectProgressSupportSkipsNotificationTerminals(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"iTerm2":  {"TERM_PROGRAM": "iTerm.app"},
		"Ghostty": {"TERM_PROGRAM": "ghostty", "GHOSTTY_RESOURCES_DIR": "/x"},
	} {
		t.Run(name, func(t *testing.T) {
			neutralTerminalEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if DetectProgressSupport() {
				t.Errorf("%s must NOT be auto-enabled: it reads a bare OSC 9 as a "+
					"notification, so an unsupported 9;4 becomes a toast on every turn", name)
			}
		})
	}
}

// A multiplexer either swallows the sequence or rewrites it; either way
// it never reaches the taskbar we would be claiming to drive.
func TestDetectProgressSupportSkipsMultiplexers(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"tmux":          {"TMUX": "/tmp/tmux-1000/default,123,0", "WT_SESSION": "x"},
		"screen (STY)":  {"STY": "1234.pts-0.host", "WT_SESSION": "x"},
		"screen (TERM)": {"TERM": "screen-256color", "WT_SESSION": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			neutralTerminalEnv(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			if DetectProgressSupport() {
				t.Errorf("%s should not be auto-enabled: the sequence does not reach the outer terminal", name)
			}
		})
	}
}

func TestDetectProgressSupportSkipsDumbTerminals(t *testing.T) {
	for _, term := range []string{"", "dumb"} {
		neutralTerminalEnv(t)
		t.Setenv("TERM", term)
		t.Setenv("WT_SESSION", "x") // would otherwise pass
		if DetectProgressSupport() {
			t.Errorf("TERM=%q should not be auto-enabled", term)
		}
	}
}
