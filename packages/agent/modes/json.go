package modes

import (
	"context"
	"encoding/json"
	"io"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// RunJSON runs the agent to completion, writing one JSON object per
// AgentEvent as newline-delimited JSON. The schema is the canonical
// core.WireEvent — identical to the RPC event stream and the SDK's
// Event type.
func RunJSON(ctx context.Context, ag *core.Agent, prompt string, images []provider.ImageBlock, out io.Writer) error {
	enc := json.NewEncoder(out)

	var runErr error
	sink := func(ev core.AgentEvent) {
		_ = enc.Encode(core.EventToWire(ev))
	}

	if err := ag.Prompt(ctx, prompt, images, sink); err != nil {
		runErr = err
	}

	if runErr != nil {
		_ = enc.Encode(core.WireEvent{Type: "error", Error: runErr.Error()})
	}
	return runErr
}

// EventToJSON converts an AgentEvent to a JSON-friendly map using the
// canonical core.WireEvent schema. Exported for the RPC mode and the
// swarm child emitter, which splice the fields into their own
// envelopes; new consumers should prefer core.EventToWire directly.
func EventToJSON(ev core.AgentEvent) map[string]any {
	return core.EventToWire(ev).Map()
}

// ContentToJSON serialises a transcript content slice into the
// canonical wire-block form. Exported alongside EventToJSON for the
// RPC mode.
func ContentToJSON(blocks []provider.Content) []core.WireBlock {
	return core.ContentToWire(blocks)
}
