package tui

import (
	"strconv"
	"strings"
)

// summarizeToolNames renders the tool names of a run with counts. It is the
// canonical implementation of the transcript "tool group" summary and MUST
// stay byte-for-byte identical to the web UI's summarizeToolNames
// (packages/agent/web/client/src/toolsummary.ts) — both are pinned to the
// shared golden fixtures in testdata/tool_summary_golden.json so the two can't
// drift as the format evolves.
//
// names is the tool names of one run in first-seen order. The result:
//   - counts occurrences per name, preserving first-seen order (not sorted);
//   - renders each as "name ×N" when N>1, else "name" (× is U+00D7);
//   - keeps the first 4 distinct names + ", …" when there are more than 4
//     (… is U+2026), otherwise joins all with ", ".
func summarizeToolNames(names []string) string {
	order := make([]string, 0, len(names))
	counts := make(map[string]int, len(names))
	for _, n := range names {
		if _, seen := counts[n]; !seen {
			order = append(order, n)
		}
		counts[n]++
	}
	parts := make([]string, 0, len(order))
	for _, n := range order {
		if counts[n] > 1 {
			parts = append(parts, n+" ×"+strconv.Itoa(counts[n]))
		} else {
			parts = append(parts, n)
		}
	}
	if len(parts) > 4 {
		return strings.Join(parts[:4], ", ") + ", …"
	}
	return strings.Join(parts, ", ")
}
