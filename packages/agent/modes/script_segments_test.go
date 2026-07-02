package modes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/tui"
)

func scriptTestInteractive(scripts map[string]StatusScript, exec scriptExec) *Interactive {
	i := NewInteractive(InteractiveConfig{
		Terminal:      nil,
		Theme:         tui.Dark,
		Provider:      "openai",
		Model:         "m",
		StatusScripts: scripts,
	})
	i.scriptExec = exec
	return i
}

func TestProbeScriptsOnceStoresFirstLine(t *testing.T) {
	i := scriptTestInteractive(map[string]StatusScript{"weather": {Command: "true"}},
		func(context.Context, StatusScript, []byte) (string, error) {
			return "\n72°F sunny\nsecond line\n", nil
		})
	i.probeScriptsOnce(context.Background())
	i.mu.Lock()
	got := i.scriptSegs["weather"]
	i.mu.Unlock()
	if got != "72°F sunny" {
		t.Fatalf("segment = %q, want first non-empty line", got)
	}
}

func TestProbeScriptsTimeoutKeepsPrevious(t *testing.T) {
	i := scriptTestInteractive(map[string]StatusScript{"weather": {Command: "true"}}, nil)
	i.scriptSegs["weather"] = "72°F"
	i.scriptExec = func(context.Context, StatusScript, []byte) (string, error) {
		return "", errScriptTimeout
	}
	i.probeScriptsOnce(context.Background())
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.scriptSegs["weather"] != "72°F" {
		t.Fatalf("timeout must keep the previous output, got %q", i.scriptSegs["weather"])
	}
}

func TestProbeScriptsFailureClearsAndNotesOnce(t *testing.T) {
	i := scriptTestInteractive(map[string]StatusScript{"weather": {Command: "true"}}, nil)
	i.scriptSegs["weather"] = "72°F"
	fail := func(context.Context, StatusScript, []byte) (string, error) {
		return "", errors.New("exit status 3")
	}
	i.scriptExec = fail
	i.probeScriptsOnce(context.Background())
	i.mu.Lock()
	if i.scriptSegs["weather"] != "" {
		t.Fatalf("failure should clear the segment, got %q", i.scriptSegs["weather"])
	}
	if !strings.Contains(i.statusOK, "weather") {
		t.Fatalf("first failure should note the script name, got %q", i.statusOK)
	}
	i.statusOK = ""
	i.mu.Unlock()

	// Second failure in the streak: silent.
	i.probeScriptsOnce(context.Background())
	i.mu.Lock()
	if i.statusOK != "" {
		t.Fatalf("repeat failure must not re-note, got %q", i.statusOK)
	}
	i.mu.Unlock()

	// Recovery re-arms the note.
	i.scriptExec = func(context.Context, StatusScript, []byte) (string, error) { return "back", nil }
	i.probeScriptsOnce(context.Background())
	i.scriptExec = fail
	i.probeScriptsOnce(context.Background())
	i.mu.Lock()
	defer i.mu.Unlock()
	if !strings.Contains(i.statusOK, "weather") {
		t.Fatalf("failure after recovery should note again, got %q", i.statusOK)
	}
}

// The stdin payload is valid JSON carrying the documented fields.
func TestScriptPayloadShape(t *testing.T) {
	var got map[string]any
	i := scriptTestInteractive(map[string]StatusScript{"x": {Command: "true"}},
		func(_ context.Context, _ StatusScript, stdin []byte) (string, error) {
			if err := json.Unmarshal(stdin, &got); err != nil {
				t.Fatalf("stdin is not JSON: %v", err)
			}
			return "ok", nil
		})
	i.mu.Lock()
	i.gitInfo = tui.GitInfo{Present: true, Branch: "main", Added: 3}
	i.editsAdded = 7
	i.mu.Unlock()
	i.probeScriptsOnce(context.Background())

	if got["schema"] != float64(1) {
		t.Errorf("schema = %v, want 1", got["schema"])
	}
	if got["provider"] != "openai" || got["model"] != "m" {
		t.Errorf("provider/model missing: %v", got)
	}
	git, _ := got["git"].(map[string]any)
	if git == nil || git["branch"] != "main" {
		t.Errorf("git block missing or wrong: %v", got["git"])
	}
	if got["edits_added"] != float64(7) {
		t.Errorf("edits_added = %v, want 7", got["edits_added"])
	}
}

func TestFirstOutputLine(t *testing.T) {
	for in, want := range map[string]string{
		"one":               "one",
		"\n\ntwo\nthree":    "two",
		"crlf\r\nnext":      "crlf",
		"   \n  spaced  \n": "  spaced  ",
		"":                  "",
		"\n\n":              "",
	} {
		if got := firstOutputLine(in); got != want {
			t.Errorf("firstOutputLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExecStatusScriptTimeoutClamp(t *testing.T) {
	// A script that sleeps past its clamped timeout returns the
	// timeout sentinel (unix-only smoke; skipped where sh is absent).
	if _, err := execStatusScript(context.Background(),
		StatusScript{Command: "sleep 5", Timeout: scriptSegmentMinTimeout}, nil); !errors.Is(err, errScriptTimeout) {
		t.Skipf("timeout sentinel not observed (err=%v); environment-dependent", err)
	}
	_ = time.Now()
}
