package core

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// prefixSpyClient records every request it is handed and streams a usable
// summary, so a test can assert WHICH client and model a compaction was sent
// to — the question the whole prefix snapshot exists to answer.
type prefixSpyClient struct {
	name string

	mu   sync.Mutex
	reqs []provider.Request
}

func (c *prefixSpyClient) Name() string { return c.name }

func (c *prefixSpyClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.name, Model: req.Model}
		// Non-empty text: compactHeld rejects an empty summary.
		out <- provider.EventTextDelta{Delta: "## Goal\nkeep going"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "## Goal\nkeep going"}},
		}}
	}()
	return out, nil
}

func (c *prefixSpyClient) calls() []provider.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]provider.Request(nil), c.reqs...)
}

// A compaction triggered by a model switch must be summarized on the OUTGOING
// model — the one that still has the transcript warm in its prompt cache.
//
// Model and Client are read per STEP, not pinned (oneTurn), so a /model switch
// lands on the very next request and cannot be gated. That means by the time a
// compaction runs in response to the switch, the agent already holds the
// INCOMING model. Summarizing on it would send a cold, transcript-sized read to
// a model that has never seen this conversation — paying in full the exact bill
// the compaction was offered to avoid, and turning the guard into a footgun of
// its own.
func TestCompactionTargetsTheOutgoingModel(t *testing.T) {
	outgoing := &prefixSpyClient{name: "outgoing"}
	incoming := &prefixSpyClient{name: "incoming"}
	a := NewAgent(outgoing, "outgoing-model", "system", Registry{})

	// One real turn: now the outgoing model has this conversation cached, and
	// the agent has retained the prefix that produced that cache.
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}

	// The switch. The agent's own fields have already moved on.
	a.SetClientAndModel(incoming, "incoming-model")

	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}

	sent := outgoing.calls()
	if len(sent) != 2 {
		t.Fatalf("outgoing client saw %d requests; want 2 (the turn, then the compaction)", len(sent))
	}
	if got := sent[1].Model; got != "outgoing-model" {
		t.Errorf("compaction went to model %q; want %q — the summary must be billed against the warm cache", got, "outgoing-model")
	}
	if n := len(incoming.calls()); n != 0 {
		t.Errorf("incoming client saw %d requests; want 0 — the incoming model has never seen this transcript, so summarizing there is a full-price cold read", n)
	}
}

// With nothing dispatched yet — a resumed session compacted before its first
// turn — there is no warm cache to aim at, so the live agent fields are both
// the only available answer and the right one.
func TestCompactionBeforeFirstDispatchUsesLiveAgentFields(t *testing.T) {
	live := &prefixSpyClient{name: "live"}
	a := NewAgent(live, "live-model", "system", Registry{})
	a.SetMessages([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "resumed from disk"}},
	}})

	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}

	sent := live.calls()
	if len(sent) != 1 {
		t.Fatalf("live client saw %d requests; want 1 (the compaction)", len(sent))
	}
	if got := sent[0].Model; got != "live-model" {
		t.Errorf("compaction went to model %q; want %q", got, "live-model")
	}
}

// refusedClient fails the way a real client fails a request the provider never
// accepted: the error comes back from Stream itself, not through the event
// channel. This is not fake convenience — anthropicClient makes the POST
// synchronously inside Stream and returns a non-nil error for ANY non-200, so a
// channel only ever comes back after a 200 OK. That is what makes "Stream
// returned nil" a sound proxy for "the provider cached this prefix".
type refusedClient struct{ calls int32 }

func (c *refusedClient) Name() string { return "refused" }

func (c *refusedClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	return nil, provider.NewHTTPError("refused", 400, "", "model not found")
}

// A dispatch the provider never accepted cached nothing, so it must not become
// the compaction target: the retained prefix has to keep pointing at the last
// request that actually landed. Otherwise one bad /model switch — a typo'd id,
// an endpoint that 400s the tool schema — would silently redirect every later
// compaction to a model that never saw the conversation, which is both a cold
// read and a summary from the wrong model.
func TestFailedDispatchDoesNotBecomeTheCompactionTarget(t *testing.T) {
	good := &prefixSpyClient{name: "good"}
	a := NewAgent(good, "good-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond
	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt returned %v", err)
	}

	// A swap to an endpoint that rejects the request outright.
	dead := &refusedClient{}
	a.SetClientAndModel(dead, "dead-model")
	if err := a.Prompt(context.Background(), "does this land?", nil, nil); err == nil {
		t.Fatal("Prompt against the refusing client returned nil; want the dispatch error")
	}

	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}

	sent := good.calls()
	if len(sent) != 2 {
		t.Fatalf("good client saw %d requests; want 2 (the turn, then the compaction)", len(sent))
	}
	if got := sent[1].Model; got != "good-model" {
		t.Errorf("compaction went to model %q; want %q — a request that never reached a provider cached nothing", got, "good-model")
	}
}

// compactHeld used to read a.Model / a.Client / a.Temperature unlocked while
// SetModel and SetClientAndModel write them under a.mu. Compact's single-flight
// guard excludes other TURNS, not a host's model swap — and under the prefix-
// change guard, a swap concurrent with a compaction stops being exotic and
// becomes the headline case. Snapshotting the prefix as one struct under one
// lock closes it. Run with -race; this is a no-op otherwise.
func TestCompactIsRaceFreeAgainstAConcurrentModelSwap(t *testing.T) {
	client := &prefixSpyClient{name: "spy"}
	a := NewAgent(client, "model-0", "system", Registry{})
	a.SetMessages([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "a transcript worth condensing"}},
	}})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				a.SetModel(fmt.Sprintf("model-%d", i))
			}
		}
	}()

	// Let the writer get going, so its writes are live in the race detector's
	// shadow history when the compaction reads the prefix.
	time.Sleep(2 * time.Millisecond)

	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("Compact returned %v", err)
	}
	close(stop)
	wg.Wait()
}
