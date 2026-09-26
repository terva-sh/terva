package build

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/tools/tasks"
	"terva.sh/terva/packages/agent/tools/tasks/tasktool"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

var updateFrameGolden = flag.Bool("update-frame-golden", false, "rewrite testdata/frame_request.golden from the current code")

// recordingClient keeps every request it is sent and answers each with a short
// final reply.
type recordingClient struct {
	mu   sync.Mutex
	reqs []provider.Request
}

func (c *recordingClient) Name() string { return "openai" }

func (c *recordingClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	out := make(chan provider.Event, 1)
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}}
	close(out)
	return out, nil
}

// The system prompt and the ephemeral tail terva puts on the wire are pinned
// byte for byte, through the real build path: the resolved system prompt, a
// task card, an extension card, and triggered lore, at build and again after a
// live lore rewire. The golden file was captured before the engine took a
// Frame from a ContextAssembler (TKT-01M35WJZ9), which changed how these bytes
// are assembled and must not change the bytes. Regenerate it with
// -update-frame-golden only for a change that means to alter the prompt.
func TestTheWireRequestIsUnchangedByteForByte(t *testing.T) {
	ag, args := loreAgent(t)
	ctrl := tasktool.New(tasks.NewStore(nil, "agent"))
	if _, err := ctrl.Store().Create([]tasks.CreateSpec{{Title: "ship the thing", ActiveForm: "shipping the thing"}}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	tail := EphemeralTail{Ext: func() string { return "EXT-CARD" }, Tasks: ctrl}
	WireEphemeralTail(ag, tail)
	rec := &recordingClient{}
	ag.SetClientAndModel(rec, ag.Model())

	if err := ag.Prompt(context.Background(), "and the dragon's hoard?", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	if rr := RewireLoreContext(ag, args, tail); rr == nil {
		t.Fatal("RewireLoreContext reported no resolve")
	}
	if err := ag.Prompt(context.Background(), "one more dragon question", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	if len(rec.reqs) != 2 {
		t.Fatalf("sent %d requests, want 2", len(rec.reqs))
	}
	var b strings.Builder
	for i, req := range rec.reqs {
		b.WriteString("===== request " + string(rune('1'+i)) + " system =====\n")
		b.WriteString(req.System)
		b.WriteString("\n===== request " + string(rune('1'+i)) + " tail =====\n")
		b.WriteString(req.EphemeralContext)
		b.WriteString("\n")
	}
	got := b.String()
	// ReplaceAll with an empty old string inserts the new one between every
	// rune, which would turn the comparison into noise, so an empty path is a
	// broken fixture rather than something to normalize.
	home := os.Getenv("TERVA_HOME")
	if args.CWD == "" || home == "" {
		t.Fatalf("the fixture must set both paths it normalizes: cwd %q, TERVA_HOME %q", args.CWD, home)
	}
	got = strings.ReplaceAll(got, args.CWD, "<CWD>")
	got = strings.ReplaceAll(got, home, "<TERVA_HOME>")
	got = normalizeGoldenPaths(got)
	got = regexp.MustCompile(`Current date: \d{4}-\d{2}-\d{2}`).ReplaceAllString(got, "Current date: <DATE>")

	golden := filepath.Join("testdata", "frame_request.golden")
	if *updateFrameGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("the wire request changed. Diff it against %s; rerun with -update-frame-golden only if the change is meant:\n%s", golden, got)
	}
}
