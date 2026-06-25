package tui

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestUsageHint(t *testing.T) {
	cases := []struct {
		name      string
		windows   []provider.UsageWindow
		wantText  string // substring expected in the hint; "" means empty hint
		wantColor int    // sgrFG code expected; 0 to skip
	}{
		{name: "nil windows", windows: nil, wantText: ""},
		{
			name:     "below threshold stays silent",
			windows:  []provider.UsageWindow{{Label: "5h", UsedPercent: 50}},
			wantText: "",
		},
		{
			name:      "at threshold surfaces, warning color",
			windows:   []provider.UsageWindow{{Label: "5h", UsedPercent: 80}},
			wantText:  "5h 80%",
			wantColor: Dark.Warning,
		},
		{
			name: "busiest of several wins",
			windows: []provider.UsageWindow{
				{Label: "5h", UsedPercent: 42},
				{Label: "weekly", UsedPercent: 88},
			},
			wantText:  "weekly 88%",
			wantColor: Dark.Warning,
		},
		{
			name:      "above 90 is error color",
			windows:   []provider.UsageWindow{{Label: "weekly", UsedPercent: 95}},
			wantText:  "weekly 95%",
			wantColor: Dark.Error,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usageHint(Dark, tc.windows)
			if tc.wantText == "" {
				if got != "" {
					t.Fatalf("usageHint = %q, want empty", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantText) {
				t.Errorf("usageHint = %q, want substring %q", got, tc.wantText)
			}
			if tc.wantColor != 0 && !strings.Contains(got, sgrFG(tc.wantColor)) {
				t.Errorf("usageHint = %q, want color %s", got, sgrFG(tc.wantColor))
			}
		})
	}
}

// TestStatusBarShowsUsageHint proves the hint reaches the rendered bar
// when a window is high, and stays absent when it isn't.
func TestStatusBarShowsUsageHint(t *testing.T) {
	high := StatusBar(StatusBarParams{
		Theme:        Dark,
		Provider:     "openai-codex",
		Model:        "gpt-5.5",
		CWD:          "/tmp/x",
		UsageWindows: []provider.UsageWindow{{Label: "weekly", UsedPercent: 88}},
	})
	if !strings.Contains(strings.Join(high, "\n"), "weekly 88%") {
		t.Errorf("status bar missing usage hint:\n%s", strings.Join(high, "\n"))
	}

	low := StatusBar(StatusBarParams{
		Theme:        Dark,
		Provider:     "openai-codex",
		Model:        "gpt-5.5",
		CWD:          "/tmp/x",
		UsageWindows: []provider.UsageWindow{{Label: "weekly", UsedPercent: 12}},
	})
	if strings.Contains(strings.Join(low, "\n"), "weekly") {
		t.Errorf("status bar showed a hint for a low window:\n%s", strings.Join(low, "\n"))
	}
}
