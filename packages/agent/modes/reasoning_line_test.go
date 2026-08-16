package modes

import "testing"

// reasoningLineCases are shared with the panel's vitest suite
// (web/client/src/platform/conversation/reasoning.test.ts), which asserts the
// same inputs produce the same outputs. The two renderers show the same wire
// text, and a user who watches one and then the other must not find the
// provider's markup surviving in only one of them.
var reasoningLineCases = []struct {
	name string
	in   string
	want string
}{
	{"empty", "", ""},
	{"plain headline", "**Inspecting commit before push**", "Inspecting commit before push"},
	{"only the current section survives", "**First step**\n\n**Second step**", "Second step"},
	{"three sections keep the last", "**A**\n\n**B**\n\n**C**", "C"},
	{"single newlines inside a section collapse", "Reading the file\nthen the handler", "Reading the file then the handler"},
	{"prose is squashed to one line", "Let me analyze:\n\n1. first\n2. second", "1. first 2. second"},
	{"runs of whitespace collapse", "  spaced   out  ", "spaced out"},
	{"carriage returns do not survive", "line one\r\nline two", "line one line two"},
	{"a trailing boundary yields nothing", "**Done thinking**\n\n", ""},
	{"bold inside prose is stripped", "checking **api.go** now", "checking api.go now"},
}

func TestReasoningLineText(t *testing.T) {
	for _, tc := range reasoningLineCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasoningLineText(tc.in); got != tc.want {
				t.Errorf("reasoningLineText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The row is ONE row, whatever arrives. A summary is usually a short headline,
// but the same field carries multi-paragraph prose from other providers — and
// this row sits between the transcript and the editor, where a second line
// would push the input off screen.
func TestReasoningLineIsAlwaysASingleRow(t *testing.T) {
	huge := ""
	for i := 0; i < 500; i++ {
		huge += "thinking about the problem "
	}
	rows := reasoningRows(testTheme(), reasoningLineText(huge), 80)
	if len(rows) != 1 {
		t.Fatalf("rendered %d rows; want exactly 1", len(rows))
	}
}

// Nothing to say, nothing drawn — no blank row reserving space for a model that
// never sends a summary (which is every provider but two).
func TestReasoningRowsAreAbsentWhenThereIsNoSummary(t *testing.T) {
	if rows := reasoningRows(testTheme(), "", 80); rows != nil {
		t.Errorf("rendered %d rows for an empty summary; want none", len(rows))
	}
}
