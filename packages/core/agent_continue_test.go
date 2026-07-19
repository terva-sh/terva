package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// prefillFakeClient advertises ContinuesAssistantPrefill and streams a fixed
// continuation, capturing the request it was handed so the test can assert the
// prefill shape (trailing assistant, no ephemeral block).
type prefillFakeClient struct {
	cont    string
	lastReq provider.Request
}

func (c *prefillFakeClient) Name() string { return "prefill-fake" }

func (c *prefillFakeClient) Capabilities() provider.ClientCapabilities {
	return provider.ClientCapabilities{ContinuesAssistantPrefill: true}
}

func (c *prefillFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.lastReq = req
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "prefill-fake", Model: req.Model}
		out <- provider.EventTextDelta{Delta: c.cont}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.cont}},
		}}
	}()
	return out, nil
}

// A plain fake with no prefill capability, for the unsupported-provider guard.
type noPrefillFakeClient struct{}

func (c *noPrefillFakeClient) Name() string { return "no-prefill-fake" }
func (c *noPrefillFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "no-prefill-fake", Model: req.Model}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "x"}}}}
	}()
	return out, nil
}

// ContinueAssistant extends the trailing assistant message in place: the
// continuation merges onto it (no new message), the ephemeral tail is suppressed
// so the assistant message is the prefill (the last request message), and the
// merged result is stashed for the caller to persist.
func TestContinueAssistantMergesInPlace(t *testing.T) {
	client := &prefillFakeClient{cont: " and vanished into the trees."}
	a := NewAgent(client, "fake-model", "system", Registry{})
	// A live steering tail that a normal turn WOULD send — the continue must
	// suppress it so the assistant message is genuinely last.
	a.ContextProvider = func() string { return "STEERING NOTE" }
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Tell me a story."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "The knight rode on,"}}},
	})

	if err := a.ContinueAssistant(context.Background(), nil); err != nil {
		t.Fatalf("ContinueAssistant: %v", err)
	}

	// The request that reached the provider: ephemeral suppressed, and the
	// trailing message is the assistant prefill.
	if client.lastReq.EphemeralContext != "" {
		t.Errorf("ephemeral not suppressed for a continue turn: %q", client.lastReq.EphemeralContext)
	}
	rm := client.lastReq.Messages
	if len(rm) == 0 || rm[len(rm)-1].Role != provider.RoleAssistant {
		t.Fatalf("request does not end in the assistant prefill: %+v", rm)
	}

	// The transcript grew IN PLACE — still two messages, the last one extended.
	msgs := a.Messages()
	if len(msgs) != 2 {
		t.Fatalf("message count = %d; want 2 (merge, not append)", len(msgs))
	}
	if got := extractText(msgs[1]); got != "The knight rode on, and vanished into the trees." {
		t.Errorf("merged text = %q", got)
	}

	// The merge is stashed for the caller to persist as an AmendReplace.
	idx, merged, ok := a.ConsumeContinueResult()
	if !ok || idx != 1 {
		t.Fatalf("ConsumeContinueResult = (%d, _, %v); want (1, _, true)", idx, ok)
	}
	if got := extractText(merged); got != "The knight rode on, and vanished into the trees." {
		t.Errorf("stashed merged text = %q", got)
	}
	// Consuming clears it.
	if _, _, ok := a.ConsumeContinueResult(); ok {
		t.Error("continue result not cleared after consume")
	}
	// The continue flag was cleared, so a normal turn sends ephemeral again.
	if a.continuePrefill {
		t.Error("continuePrefill left set after ContinueAssistant")
	}
}

// ContinueAssistant guards: no trailing assistant, and a provider that does not
// continue prefills.
func TestContinueAssistantGuards(t *testing.T) {
	// No trailing assistant message (ends in a user turn).
	a := NewAgent(&prefillFakeClient{cont: "x"}, "fake-model", "system", Registry{})
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
	})
	if err := a.ContinueAssistant(context.Background(), nil); err != ErrNoAssistantToContinue {
		t.Errorf("continue with no trailing assistant = %v; want ErrNoAssistantToContinue", err)
	}

	// A provider that does not support prefill continuation.
	b := NewAgent(&noPrefillFakeClient{}, "fake-model", "system", Registry{})
	b.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "there"}}},
	})
	if err := b.ContinueAssistant(context.Background(), nil); err != ErrContinueUnsupported {
		t.Errorf("continue on a non-prefill provider = %v; want ErrContinueUnsupported", err)
	}
	if b.ContinuesAssistantPrefill() {
		t.Error("noPrefillFakeClient must not report ContinuesAssistantPrefill")
	}
}

// mergeContinuation joins the seam text blocks (trimming the base's trailing
// whitespace) and appends any remaining continuation blocks.
func TestMergeContinuation(t *testing.T) {
	base := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "The knight rode on,  \n"}}}
	cont := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: " and vanished."}}}
	got := extractText(mergeContinuation(base, cont))
	if got != "The knight rode on, and vanished." {
		t.Errorf("merge = %q; want trimmed seam", got)
	}
}
