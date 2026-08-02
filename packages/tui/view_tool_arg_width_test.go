package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolArgWidthDefault(t *testing.T) {
	t.Setenv("TERVA_TOOL_ARG_WIDTH", "")
	if got := toolArgWidth(); got != defaultToolArgWidth {
		t.Fatalf("toolArgWidth() with unset env = %d, want %d", got, defaultToolArgWidth)
	}
}

func TestToolArgWidthEnvOverride(t *testing.T) {
	t.Setenv("TERVA_TOOL_ARG_WIDTH", "120")
	if got := toolArgWidth(); got != 120 {
		t.Fatalf("toolArgWidth() = %d, want 120", got)
	}
}

func TestToolArgWidthIgnoresInvalid(t *testing.T) {
	cases := []string{"nope", "0", "10", "501", "-5", "12.5"}
	for _, c := range cases {
		t.Setenv("TERVA_TOOL_ARG_WIDTH", c)
		if got := toolArgWidth(); got != defaultToolArgWidth {
			t.Fatalf("toolArgWidth() with %q = %d, want default %d", c, got, defaultToolArgWidth)
		}
	}
}

func TestShortArgsTruncatesAtDefaultWidth(t *testing.T) {
	t.Setenv("TERVA_TOOL_ARG_WIDTH", "")
	long := strings.Repeat("a", 200)
	raw := json.RawMessage(`{"command":"` + long + `"}`)
	got := ShortArgs("web_answer", raw)
	if len(got) != defaultToolArgWidth {
		t.Fatalf("ShortArgs length = %d, want %d (%q)", len(got), defaultToolArgWidth, got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("ShortArgs should end with ellipsis, got %q", got)
	}
}

func TestShortArgsRespectsWiderWidth(t *testing.T) {
	t.Setenv("TERVA_TOOL_ARG_WIDTH", "120")
	long := strings.Repeat("a", 200)
	raw := json.RawMessage(`{"command":"` + long + `"}`)
	got := ShortArgs("web_answer", raw)
	if len(got) != 120 {
		t.Fatalf("ShortArgs length = %d, want 120", len(got))
	}
}

func TestShortArgsNoTruncationWhenShort(t *testing.T) {
	t.Setenv("TERVA_TOOL_ARG_WIDTH", "120")
	raw := json.RawMessage(`{"command":"short query"}`)
	got := ShortArgs("web_answer", raw)
	if got != "short query" {
		t.Fatalf("ShortArgs = %q, want %q", got, "short query")
	}
}

// A tool whose subject is neither a file nor a command still has one. The
// convene tool spends six sub-agent turns and minutes of wall clock on a
// QUESTION, and with no primary key to match, its header fell back to dumping
// the arguments — so the box read `{"class":"advisory","converge":true,
// "evidence":"Reposi…`, with the actual question cut off behind the flags that
// only qualify it.
func TestShortArgsPrefersTheQuestion(t *testing.T) {
	args := json.RawMessage(`{"class":"advisory","converge":true,"evidence":"a very long evidence packet","question":"Should we merge the reconnect fix before the cut?"}`)
	got := ShortArgs("raati_convene", args)
	if !strings.HasPrefix(got, "Should we merge the reconnect fix") {
		t.Errorf("header = %q, want it to lead with the question", got)
	}
	if strings.Contains(got, "converge") || strings.Contains(got, "evidence") {
		t.Errorf("header still dumps the qualifying args: %q", got)
	}
	// A path still wins where one exists — this adds a fallback, it does not
	// reorder the tools that already had a subject.
	both := json.RawMessage(`{"path":"/tmp/x.go","question":"why?"}`)
	if got := ShortArgs("read", both); got != "/tmp/x.go" {
		t.Errorf("path must still win: %q", got)
	}
}
