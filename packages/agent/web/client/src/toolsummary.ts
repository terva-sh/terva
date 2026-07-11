// summarizeToolNames renders the tool names of a run with counts. It is the
// canonical web implementation of the transcript "tool group" summary and MUST
// stay byte-for-byte identical to the Go summarizer
// (packages/tui/toolsummary.go). Both are pinned to the shared golden fixtures
// in packages/tui/testdata/tool_summary_golden.json (loaded by this file's
// vitest and by the Go golden test) so the two implementations can't drift.
//
// names is the tool names of one run in first-seen order. The result:
//   - counts occurrences per name, preserving first-seen order (Map keeps it);
//   - renders each as "name ×N" when N>1, else "name" (× is U+00D7);
//   - keeps the first 4 distinct names + ", …" when there are more than 4
//     (… is U+2026), otherwise joins all with ", ".
export function summarizeToolNames(names: string[]): string {
  const counts = new Map<string, number>()
  for (const n of names) counts.set(n, (counts.get(n) ?? 0) + 1)
  const parts = [...counts.entries()].map(([name, c]) => (c > 1 ? `${name} ×${c}` : name))
  return parts.length > 4 ? parts.slice(0, 4).join(', ') + ', …' : parts.join(', ')
}
