package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// benchDeepTurns is the depth of the synthetic session the deep-session
// benchmark runs against: each turn is four messages, so this is ~1000
// messages of history.
const benchDeepTurns = 250

// benchNoopClient is a fake provider that finishes every turn immediately with
// no tool use — a stand-in agent so a benchmark exercises the real turn and
// context-assembly path without a live model. Events are pre-buffered (no
// goroutine) for deterministic timing.
type benchNoopClient struct{}

func (benchNoopClient) Name() string { return "bench" }

func (benchNoopClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	out <- provider.EventStart{Provider: "bench", Model: req.Model}
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}}
	close(out)
	return out, nil
}

// benchDeepMessages builds a realistically deep session history: `turns` rounds
// of user → assistant(text + tool call) → tool result (a sizable block) →
// assistant(text). Deep sessions churn on the tool-result blocks re-serialized
// into every subsequent request, so the fixture leans on those.
func benchDeepMessages(turns int) []provider.Message {
	toolOut := strings.Repeat("  scanned a line with enough detail to be non-trivial.\n", 30) // ~1.6 KiB
	msgs := make([]provider.Message, 0, turns*4)
	for i := range turns {
		id := fmt.Sprintf("call_%04d", i)
		msgs = append(msgs,
			provider.Message{Role: provider.RoleUser, Content: []provider.Content{
				provider.TextBlock{Text: fmt.Sprintf("Step %d: inspect the module and report what changed.", i)},
			}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
				provider.TextBlock{Text: fmt.Sprintf("Reading module %d to check.", i)},
				provider.ToolCallBlock{ID: id, Name: "read", Arguments: json.RawMessage(fmt.Sprintf(`{"path":"pkg/mod%04d.go"}`, i))},
			}},
			provider.Message{Role: provider.RoleTool, Content: []provider.Content{
				provider.ToolResultBlock{CallID: id, Content: []provider.Content{provider.TextBlock{Text: toolOut}}},
			}},
			provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
				provider.TextBlock{Text: fmt.Sprintf("Step %d looks fine.", i)},
			}},
		)
	}
	return msgs
}

// BenchmarkDeepSessionTurn measures one turn's critical path — assembling the
// provider request from the full history, the loop overhead, appending the
// reply — on an already-deep session. A fake agent (benchNoopClient) stands in
// for the model, so what's measured is terva's per-turn cost as a function of
// session depth: the path that historically churned on long sessions. The
// history is truncated back to its seeded depth after each turn (in-package
// access to a.messages, O(1), no realloc) so every timed turn runs at the same
// fixed depth and the alloc numbers reflect the turn alone.
func BenchmarkDeepSessionTurn(b *testing.B) {
	history := benchDeepMessages(benchDeepTurns)
	a := NewAgent(benchNoopClient{}, "bench-model", "you are a benchmark agent", Registry{})
	a.SetMessages(history)
	base := len(a.messages)

	b.ReportAllocs()
	for b.Loop() {
		if err := a.Prompt(context.Background(), "continue", nil, nil); err != nil {
			b.Fatal(err)
		}
		a.messages = a.messages[:base] // drop the turn just appended; hold depth fixed
	}
}

// BenchmarkDescribeSessions measures the session-listing scan (the /sessions
// picker and the startup scan) over a directory of many sessions. The cost
// grows with session count, so it's worth a repeatable number before a release.
func BenchmarkDescribeSessions(b *testing.B) {
	root := testsupport.TempDir(b)
	const cwd = "/bench/ws"
	const sessions, msgsPer = 120, 24
	for s := range sessions {
		sess, err := NewSession(root, cwd, "openai-codex", "gpt-5.5", "0.0.0")
		if err != nil {
			b.Fatal(err)
		}
		for m := range msgsPer {
			if err := sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{
				provider.TextBlock{Text: fmt.Sprintf("message %d in session %d", m, s)},
			}}); err != nil {
				b.Fatal(err)
			}
		}
		if err := sess.Close(); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	for b.Loop() {
		if got := DescribeSessions(root, cwd); len(got) != sessions {
			b.Fatalf("DescribeSessions = %d summaries, want %d", len(got), sessions)
		}
	}
}
