package agent

import (
	"context"
	"errors"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// rpcDoneClient fails its first call, and waits for its context on every
// call after that.
type rpcDoneClient struct {
	calls   int
	started chan struct{}
}

func (*rpcDoneClient) Name() string { return "done" }

func (c *rpcDoneClient) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	c.calls++
	if c.calls == 1 {
		return nil, errors.New("the provider refused the request")
	}
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// A failed prompt's done carries its error, so a driver that keys on done
// alone knows the prompt failed. An aborted prompt's done carries none.
func TestRPCDoneCarriesAFailedPromptsError(t *testing.T) {
	run := startRPCQuestionRun(t, nil)
	defer run.close(t)
	client := &rpcDoneClient{started: make(chan struct{})}
	run.server.agent = coretest.NewAgent(client, "fake-model", "system", core.Registry{})

	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "p1", "type": "prompt", "message": "fail"})
	if done := nextRPCFrameUntil(t, run.out, "done"); done["error"] != "the provider refused the request" {
		t.Fatalf("done = %#v, want the prompt's error", done)
	}
	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "p2", "type": "prompt", "message": "wait"})
	<-client.started
	sendRPCQuestionCommand(t, run.in, map[string]any{"id": "a1", "type": "abort"})
	if done := nextRPCFrameUntil(t, run.out, "done"); done["error"] != nil {
		t.Fatalf("done after an abort = %#v, want no error", done)
	}
}
