package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// WithImageOutput's config is forwarded to the request only when the live model
// advertises CapImageOutput under the client's provider — so an opt-in that
// lands on a non-image model quietly does nothing, and a model swap toggles it
// with no rebuild. (captureClient is defined in agent_retry_test.go.)
func TestAgentGatesImageOutputOnCapability(t *testing.T) {
	testCatalog.SetUserModels([]provider.Model{
		{Provider: "capture", ID: "img-model", MaxOutput: 1000, Source: "user",
			Caps: map[provider.Capability]bool{provider.CapImageOutput: true}},
		{Provider: "capture", ID: "plain-model", MaxOutput: 1000, Source: "user"},
	})
	t.Cleanup(func() { testCatalog.SetUserModels(nil) })

	cfg := &provider.ImageOutputConfig{Size: "1024x1024", Quality: "low", EditHistory: 1}

	capable := &captureClient{}
	a := newTestAgent(capable, "img-model", "system", Registry{})
	a.imageOutput = cfg
	if err := a.Prompt(context.Background(), "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	if capable.lastReq.ImageOutput == nil {
		t.Fatal("capable model: ImageOutput must be forwarded")
	}

	plain := &captureClient{}
	b := newTestAgent(plain, "plain-model", "system", Registry{})
	b.imageOutput = cfg
	if err := b.Prompt(context.Background(), "hi", nil, nil); err != nil {
		t.Fatal(err)
	}
	if plain.lastReq.ImageOutput != nil {
		t.Fatalf("non-capable model: ImageOutput must be suppressed, got %+v", plain.lastReq.ImageOutput)
	}
}
