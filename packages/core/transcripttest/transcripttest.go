// Package transcripttest is the behavior suite a core.TranscriptStore has to
// pass. It checks the one property that makes a store a store: an agent
// resumed from what the store kept starts where the live agent left off, with
// the same messages, the same cost and context gauge, and the same activated
// tool groups.
//
// A store's own tests call Run. core runs it against MemoryTranscriptStore,
// and packages/agent/build against the JSONL session file.
package transcripttest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Store is one store under test.
type Store struct {
	core.TranscriptStore
	// Reload reads back what the store holds, the way a host would before a
	// resume. It must not disturb the store's writes.
	Reload func() (core.Transcript, error)
}

// Run runs every scenario, each against a fresh store from open.
func Run(t *testing.T, open func(t *testing.T) Store) {
	t.Helper()
	for _, sc := range []struct {
		name string
		fn   func(*testing.T, Store)
	}{
		{"an agent's turn resumes as it ran", agentTurnResumes},
		{"a compaction replaces the transcript", compactionReplaces},
		{"a clear resumes empty", clearResumesEmpty},
		{"only a turn sets the context gauge", onlyATurnSetsTheGauge},
		{"tool groups resume once each, in order", toolGroupsResume},
		{"an excluded image is dropped everywhere", excludedImageDropped},
		{"an unanswered tool call is given a result", unansweredCallRepaired},
		{"a caller's edits do not reach the store", callerEditsIsolated},
	} {
		t.Run(sc.name, func(t *testing.T) { sc.fn(t, open(t)) })
	}
}

func reload(t *testing.T, s Store) core.Transcript {
	t.Helper()
	tr, err := s.Reload()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return tr
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// agentTurnResumes runs a real turn with a tool call on an agent the store is
// attached to, then resumes a second agent from the store and compares them.
func agentTurnResumes(t *testing.T, s Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	live := newAgent()
	live.AttachTranscriptStore(s)
	if err := live.Prompt(ctx, "echo hello", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := live.PersistenceError(); err != nil {
		t.Fatalf("the store refused a write: %v", err)
	}
	// The turn must have produced what the comparison is about, or two empty
	// agents would compare equal.
	if n := len(live.Messages()); n < 4 {
		t.Fatalf("the turn left %d messages, want a prompt, a call, a result and an answer", n)
	}
	if live.Cost().InputTokens == 0 || live.LastTurnUsage().InputTokens == 0 {
		t.Fatal("the turn reported no usage, so the cost and gauge checks would prove nothing")
	}

	resumed := newAgent()
	resumed.Resume(reload(t, s))

	sameMessages(t, resumed.Messages(), live.Messages())
	if got, want := resumed.Cost(), live.Cost(); got != want {
		t.Errorf("resumed cost = %+v, live = %+v", got, want)
	}
	if got, want := resumed.LastTurnUsage(), live.LastTurnUsage(); got != want {
		t.Errorf("resumed context gauge = %+v, live = %+v", got, want)
	}
}

func compactionReplaces(t *testing.T, s Store) {
	turn := provider.Usage{InputTokens: 50_000, CostUSD: 0.1}
	must(t, s.AppendMessage(text(provider.RoleUser, "a long conversation")))
	must(t, s.AppendMessage(text(provider.RoleAssistant, "and its answer")))
	must(t, s.AppendUsage(core.UsageRecord{Kind: core.UsageTurn, Usage: turn, Cumulative: turn}))
	kept := []provider.Message{
		text(provider.RoleUser, "## Context Summary\nwe decided on the tri-state"),
		text(provider.RoleAssistant, "acknowledged"),
	}
	spend := provider.Usage{InputTokens: 40_000, CostUSD: 0.2}
	must(t, s.AppendCompaction(kept, core.CompactResult{Usage: spend}))

	tr := reload(t, s)
	sameMessages(t, tr.Messages, kept)
	if want := turn.Add(spend); tr.Cumulative != want {
		t.Errorf("cumulative = %+v, want the turn plus the compaction's own spend %+v", tr.Cumulative, want)
	}
	if tr.ResumeContext.InputTokens == 0 || tr.ResumeContext.InputTokens >= turn.InputTokens {
		t.Errorf("context gauge = %d; want the compacted transcript's size, above 0 and well under the turn's %d",
			tr.ResumeContext.InputTokens, turn.InputTokens)
	}
}

func clearResumesEmpty(t *testing.T, s Store) {
	turn := provider.Usage{InputTokens: 80_000}
	must(t, s.AppendMessage(text(provider.RoleUser, "hello")))
	must(t, s.AppendUsage(core.UsageRecord{Kind: core.UsageTurn, Usage: turn, Cumulative: turn}))
	must(t, s.AppendCompaction(nil, core.CompactResult{}))

	tr := reload(t, s)
	if len(tr.Messages) != 0 {
		t.Errorf("%d messages after a clear, want none", len(tr.Messages))
	}
	if tr.ResumeContext != (provider.Usage{}) {
		t.Errorf("context gauge = %+v after a clear, want zero", tr.ResumeContext)
	}
	if tr.Cumulative != turn {
		t.Errorf("cumulative = %+v, want %+v: a clear does not refund what was spent", tr.Cumulative, turn)
	}
}

func onlyATurnSetsTheGauge(t *testing.T, s Store) {
	turn := provider.Usage{InputTokens: 10_000}
	child := provider.Usage{InputTokens: 90_000}
	side := provider.Usage{InputTokens: 3_000}
	must(t, s.AppendMessage(text(provider.RoleUser, "hello")))
	must(t, s.AppendUsage(core.UsageRecord{Kind: core.UsageTurn, Usage: turn, Cumulative: turn}))
	must(t, s.AppendUsage(core.UsageRecord{Kind: core.UsageDelegated, Usage: child, Cumulative: turn.Add(child)}))
	must(t, s.AppendUsage(core.UsageRecord{Kind: core.UsageSideChannel, Source: "suggest", Usage: side, Cumulative: turn.Add(child).Add(side)}))

	tr := reload(t, s)
	if tr.ResumeContext != turn {
		t.Errorf("context gauge = %+v, want the last TURN's %+v, not a sub-agent's or a side channel's", tr.ResumeContext, turn)
	}
	if want := turn.Add(child).Add(side); tr.Cumulative != want {
		t.Errorf("cumulative = %+v, want every kind counted: %+v", tr.Cumulative, want)
	}
}

func toolGroupsResume(t *testing.T, s Store) {
	must(t, s.AppendMessage(text(provider.RoleUser, "hello")))
	for _, g := range []string{"browser", "db", "browser"} {
		must(t, s.AppendToolGroupActivation(g))
	}
	if got, want := reload(t, s).ActiveToolGroups, []string{"browser", "db"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tool groups = %q, want %q", got, want)
	}
}

func excludedImageDropped(t *testing.T, s Store) {
	img := provider.ImageBlock{MimeType: "image/png", Data: []byte("not really a png")}
	other := provider.ImageBlock{MimeType: "image/png", Data: []byte("a different image")}
	must(t, s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{img, other}}))
	must(t, s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{ID: "c1", Name: "screenshot", Arguments: json.RawMessage(`{}`)},
	}}))
	must(t, s.AppendMessage(provider.Message{Role: provider.RoleTool, Content: []provider.Content{
		provider.ToolResultBlock{CallID: "c1", Content: []provider.Content{img}},
	}}))
	sum := sha256.Sum256(img.Data)
	must(t, s.AppendImageExclusion(hex.EncodeToString(sum[:])))

	var kept, dropped int
	count := func(c provider.Content) {
		if b, ok := c.(provider.ImageBlock); ok {
			if string(b.Data) == string(img.Data) {
				dropped++
			} else {
				kept++
			}
		}
	}
	for _, m := range reload(t, s).Messages {
		for _, c := range m.Content {
			count(c)
			if r, ok := c.(provider.ToolResultBlock); ok {
				for _, inner := range r.Content {
					count(inner)
				}
			}
		}
	}
	if dropped != 0 {
		t.Errorf("the excluded image survived %d time(s); it must be dropped in a message and in a tool result", dropped)
	}
	if kept != 1 {
		t.Errorf("the other image appears %d time(s), want 1: exclusion is by content, not by position", kept)
	}
}

func unansweredCallRepaired(t *testing.T, s Store) {
	must(t, s.AppendMessage(text(provider.RoleUser, "run it")))
	must(t, s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{ID: "lost", Name: "bash", Arguments: json.RawMessage(`{}`)},
	}}))

	msgs := reload(t, s).Messages
	for _, m := range msgs {
		for _, c := range m.Content {
			if r, ok := c.(provider.ToolResultBlock); ok && r.CallID == "lost" {
				if !r.IsError {
					t.Error("the stub result for an interrupted call is not marked an error")
				}
				return
			}
		}
	}
	t.Errorf("no result for the interrupted call in %d messages; a provider rejects a transcript that leaves one", len(msgs))
}

// callerEditsIsolated edits a message after appending it, and a transcript
// after loading it. Neither edit may change what the next reload returns.
func callerEditsIsolated(t *testing.T, s Store) {
	img := provider.ImageBlock{MimeType: "image/png", Data: []byte("original")}
	call := provider.ToolCallBlock{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"text":"original"}`)}
	m := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{img, call},
		Meta:    map[string]string{"k": "original"},
	}
	must(t, s.AppendMessage(m))
	m.Meta["k"] = "edited"
	img.Data[0] = 'X'
	call.Arguments[2] = 'X'

	first := reload(t, s)
	check := func(when string, tr core.Transcript) {
		t.Helper()
		if len(tr.Messages) == 0 {
			t.Fatalf("%s: no messages", when)
		}
		got := tr.Messages[0]
		if got.Meta["k"] != "original" {
			t.Errorf("%s: meta = %q, want \"original\"", when, got.Meta["k"])
		}
		for _, c := range got.Content {
			switch b := c.(type) {
			case provider.ImageBlock:
				if string(b.Data) != "original" {
					t.Errorf("%s: image bytes = %q, want \"original\"", when, b.Data)
				}
			case provider.ToolCallBlock:
				if string(b.Arguments) != `{"text":"original"}` {
					t.Errorf("%s: arguments = %s, want the original", when, b.Arguments)
				}
			}
		}
	}
	check("after editing the appended message", first)

	first.Messages[0].Meta["k"] = "edited"
	for _, c := range first.Messages[0].Content {
		if b, ok := c.(provider.ImageBlock); ok {
			b.Data[0] = 'Y'
		}
	}
	check("after editing a loaded transcript", reload(t, s))
}

func text(role provider.Role, s string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: s}}}
}

// sameMessages compares transcripts. Times are compared as instants, because a
// store that serializes them keeps the instant but not the location or the
// monotonic reading. A message that arrived with no time may come back with
// one: the JSONL store stamps the write time on it, and the engine leaves the
// assistant's messages unstamped.
func sameMessages(t *testing.T, got, want []provider.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d messages, want %d:\n got  %+v\n want %+v", len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if !w.Time.IsZero() && !g.Time.Equal(w.Time) {
			t.Errorf("message %d: time %v, want %v", i, g.Time, w.Time)
		}
		g.Time, w.Time = time.Time{}, time.Time{}
		if len(g.Meta) == 0 && len(w.Meta) == 0 {
			g.Meta, w.Meta = nil, nil
		}
		if !reflect.DeepEqual(g, w) {
			t.Errorf("message %d:\n got  %#v\n want %#v", i, g, w)
		}
	}
}
