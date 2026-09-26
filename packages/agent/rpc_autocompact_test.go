package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// capturedOut keeps every line the RPC server writes.
type capturedOut struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *capturedOut) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// types returns the "type" of every event line, in order.
func (c *capturedOut) types(t *testing.T) []string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(c.buf.String()), "\n") {
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err == nil && ev.Type != "" {
			out = append(out, ev.Type)
		}
	}
	return out
}

// afterTurnPolicy says compact after a turn, and nowhere else, when due is set.
type afterTurnPolicy struct{ due bool }

func (p afterTurnPolicy) Decide(s core.CompactionState) core.CompactionDecision {
	return core.CompactionDecision{Compact: p.due && s.Point == core.CompactAfterTurn, KeepTail: 2}
}

// The RPC server's post-turn compaction had no test. It runs when the policy
// says so after a clean turn, inside the request lifecycle: compact_start and
// compact_end go out before the terminal done, so a client that waits for done
// finds the transcript already condensed. When the policy says no, nothing
// about compaction is written.
func TestRPCCompactsAfterATurnWhenThePolicySaysSo(t *testing.T) {
	for _, due := range []bool{false, true} {
		ag := coretest.NewAgent(rpcEchoClient{}, "fake-model", "sys", core.Registry{}, core.WithCompactionPolicy(afterTurnPolicy{due: due}))
		seed := make([]provider.Message, 0, 8)
		for i := range 8 {
			role := provider.RoleUser
			if i%2 == 1 {
				role = provider.RoleAssistant
			}
			seed = append(seed, provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: "filler"}}})
		}
		ag.SetMessages(seed)
		out := &capturedOut{}
		s := &rpcServer{ctx: context.Background(), agent: ag, out: out}
		s.runPrompt("1", "go", nil)

		types := strings.Join(out.types(t), " ")
		if !strings.HasSuffix(types, "done") {
			t.Fatalf("due=%v: the stream does not end with done: %s", due, types)
		}
		hasCompact := strings.Contains(types, "compact_start")
		if !due {
			if hasCompact {
				t.Errorf("the policy said no, but the server compacted: %s", types)
			}
			continue
		}
		if !strings.HasSuffix(types, "compact_start compact_end done") {
			t.Errorf("want compact_start and compact_end just before done: %s", types)
		}
		// The summary plus the policy's keep-tail of 2.
		if n := len(ag.Messages()); n != 3 {
			t.Errorf("transcript has %d messages after the post-turn compaction, want 3", n)
		}
	}
}
