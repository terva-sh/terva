package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// onePassClient is a minimal provider.Client whose every turn streams a
// fixed text reply and completes.
type onePassClient struct{ reply string }

func (c *onePassClient) Name() string { return "fake" }

func (c *onePassClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventTextDelta{Delta: c.reply}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.reply}},
		}}
	}()
	return out, nil
}

// TestWireHeadlessSessionPersistWritesTurns is the regression test for
// bot mode's silent transcript loss: the daemon opened a session, told
// extensions about it, and never appended a single turn — every restart
// forgot the whole DM conversation while claiming to persist it. The
// helper under test is what botcmd (and ACP) now wire: per-message
// durable persistence plus the session identity terva_status reports.
func TestWireHeadlessSessionPersistWritesTurns(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "prov", "fake-model", "test")
	if err != nil {
		t.Fatal(err)
	}

	ag := core.NewAgent(&onePassClient{reply: "hello from the daemon"}, "fake-model", "", core.Registry{})
	wireHeadlessSessionPersist(ag, sess)

	if id, path := ag.SessionIdentity(); id == "" || path != sess.Path {
		t.Fatalf("helper did not adopt session identity: id=%q path=%q", id, path)
	}

	if err := ag.Prompt(context.Background(), "are you keeping notes?", nil, func(core.AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "are you keeping notes?") {
		t.Errorf("session file missing the user prompt:\n%s", got)
	}
	if !strings.Contains(got, "hello from the daemon") {
		t.Errorf("session file missing the assistant reply:\n%s", got)
	}
}
