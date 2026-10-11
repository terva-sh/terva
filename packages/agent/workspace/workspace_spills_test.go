package workspace

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// bashTurnClient scripts a turn that runs one bash command and then answers.
type bashTurnClient struct {
	command string
	mu      sync.Mutex
	calls   int
}

func (c *bashTurnClient) Name() string { return "bash-turn-fake" }

func (c *bashTurnClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	args, _ := json.Marshal(map[string]any{"command": c.command})
	out := make(chan provider.Event, 1)
	go func() {
		defer close(out)
		if n == 1 {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "B1", Name: "bash", Arguments: args}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// spillRecorder runs a tool and reports the spill file its result names.
type spillRecorder struct {
	core.Tool
	paths chan string
}

func (r spillRecorder) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	res, err := r.Tool.Execute(ctx, raw, progress)
	if d, ok := res.Details.(map[string]any); ok {
		if p, _ := d["full_output_path"].(string); p != "" {
			r.paths <- p
		}
	}
	return res, err
}

// A spill that a tool keeps during a turn stays while the session lives, so the
// model can page it on a later turn, and goes when the session closes. The turn
// runs through the real agent loop, so this also proves that the turn context
// reaches the tool.
func TestSessionCloseRemovesTheSpillsOfItsTurns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tmp := testsupport.TempDir(t)
	t.Setenv("TMPDIR", tmp)
	s := newTurnTestSession(t, &bashTurnClient{command: "yes | head -n 3000"})
	rec := spillRecorder{Tool: &tools.BashTool{CWD: tmp}, paths: make(chan string, 1)}
	s.agent.SetTools(core.Registry{"bash": rec})

	if err := s.prompt("run it", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	var path string
	select {
	case path = <-rec.paths:
	case <-time.After(30 * time.Second):
		t.Fatal("the bash call kept no spill for 3000 lines of output")
	}
	waitIdle(t, s)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the spill went before the session ended: %v", err)
	}
	s.close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the spill outlived its session: %v", err)
	}
}
