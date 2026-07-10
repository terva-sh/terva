package replay

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// benchDeepRows builds a deep artificial session as replay rows: `turns` rounds
// of user → assistant(text + tool call) → tool result (a sizable block) →
// assistant(text), a usage row per turn, and one compaction checkpoint
// mid-session. This is the deep session the replay tooling reconstructs.
func benchDeepRows(turns int) []core.ReplayRow {
	toolOut := strings.Repeat("  scanned a line with enough detail to be non-trivial.\n", 30) // ~1.6 KiB
	rows := make([]core.ReplayRow, 0, turns*5+1)
	msg := func(m provider.Message) {
		rows = append(rows, core.ReplayRow{Kind: core.ReplayRowMessage, Message: m})
	}
	for i := range turns {
		id := fmt.Sprintf("call_%04d", i)
		msg(provider.Message{Role: provider.RoleUser, Content: []provider.Content{
			provider.TextBlock{Text: fmt.Sprintf("Step %d: inspect the module and report.", i)},
		}})
		msg(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: fmt.Sprintf("Reading module %d to check.", i)},
			provider.ToolCallBlock{ID: id, Name: "read", Arguments: json.RawMessage(fmt.Sprintf(`{"path":"pkg/mod%04d.go"}`, i))},
		}})
		msg(provider.Message{Role: provider.RoleTool, Content: []provider.Content{
			provider.ToolResultBlock{CallID: id, Content: []provider.Content{provider.TextBlock{Text: toolOut}}},
		}})
		msg(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: fmt.Sprintf("Step %d looks fine.", i)},
		}})
		rows = append(rows, core.ReplayRow{
			Kind:       core.ReplayRowUsage,
			Usage:      provider.Usage{InputTokens: 1000, OutputTokens: 50},
			Cumulative: provider.Usage{InputTokens: 1000 * (i + 1), OutputTokens: 50 * (i + 1)},
		})
		if i == turns/2 { // one mid-session compaction checkpoint
			rows = append(rows, core.ReplayRow{Kind: core.ReplayRowCompaction, Checkpoint: []provider.Message{
				{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "summary of the session so far"}}},
			}})
		}
	}
	return rows
}

// BenchmarkSynthesizeDeepSession measures the replay tooling reconstructing a
// deep session into its ordered frame stream — the pure rows→frames transform
// that `terva replay` and the session scrubber run. Effective mode (which
// animates the compaction) is the default and the heavier path.
func BenchmarkSynthesizeDeepSession(b *testing.B) {
	rows := benchDeepRows(250)
	opts := Options{Mode: ModeEffective, Pace: DefaultPace()}
	b.ReportAllocs()
	for b.Loop() {
		if frames := Synthesize(rows, opts); len(frames) == 0 {
			b.Fatal("no frames synthesized")
		}
	}
}
