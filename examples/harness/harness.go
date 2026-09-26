package main

import (
	"context"
	"encoding/json"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// systemPrompt is the whole frame. A host with conventions of its own writes a
// core.ContextAssembler instead, and can put per-request context in Volatile
// segments; core.StaticSystem is the one-segment case.
const systemPrompt = "You are a small assistant. Use the add tool for arithmetic " +
	"and the remember tool to keep a note. Answer in one sentence."

// hostCatalog is the example's model catalog: the built-in list, plus a row of
// its own for the scripted client, which no built-in list knows. The engine
// and the wire hold no catalog; a host passes one to both, with
// Agent.SetCatalog and provider.WithCatalog. A host with no models of its own
// can pass provider.Builtin(), which is also what an agent or client given
// none reads.
func hostCatalog() provider.ModelCatalog {
	r := provider.NewRegistry()
	r.SetUserModels([]provider.Model{{
		Provider: "scripted", ID: "scripted", DisplayName: "Scripted",
		ContextWindow: 32000, MaxOutput: 1024,
	}})
	return r
}

// newAgent wires an engine with the example's own choices and nothing of
// terva's: a static frame, two tools written here, a gate, a model catalog,
// and a transcript store. Every argument is a value; nothing is read from disk
// or the environment.
//
// The store is in memory. The engine writes each message, usage figure and
// compaction to it as they happen, and a later agent resumes from what it
// kept (see resume). A host that wants the transcript on disk or in a
// database writes its own core.TranscriptStore and runs it through
// packages/core/transcripttest. Compaction is left at its default,
// core.DefaultCompactionPolicy, the engine's neutral one.
//
// core.New takes each choice as an option, and WithGate is the one it
// requires. It returns an error rather than an agent it could not build, so a
// missing gate fails here and not on the first tool call.
func newAgent(client provider.Client, model string, catalog provider.ModelCatalog, notes *notebook, store core.TranscriptStore) (*core.Agent, error) {
	tools := core.Registry{}
	for _, t := range []core.Tool{addTool{}, rememberTool{notes: notes}} {
		tools[t.Name()] = t
	}
	return core.New(client, model,
		core.WithAssembler(core.StaticSystem(systemPrompt)),
		core.WithTools(tools),
		core.WithGate(core.GateFunc(gate)),
		core.WithCatalog(catalog),
		core.WithTranscriptStore(store),
	)
}

// resume builds a second agent that continues the conversation store kept,
// as a host does after a restart. It attaches the same store, so the new
// agent's turns land after the old one's.
func resume(client provider.Client, model string, catalog provider.ModelCatalog, notes *notebook, store *core.MemoryTranscriptStore) (*core.Agent, error) {
	agent, err := newAgent(client, model, catalog, notes, store)
	if err != nil {
		return nil, err
	}
	agent.Resume(store.Transcript())
	return agent, nil
}

// gate is the example's permission policy: it refuses to remember a note that
// mentions a password, and allows every other call. The engine hands the
// reason back to the model as the tool's error result. A real policy would
// check more than one word; this one is small enough to read at a glance.
func gate(_ context.Context, call provider.ToolCallBlock, _ core.Tool) (bool, string, json.RawMessage) {
	if call.Name != "remember" {
		return true, "", nil
	}
	var args rememberArgs
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return false, "remember: arguments are not valid JSON", nil
	}
	if strings.Contains(strings.ToLower(args.Note), "password") {
		return false, "refused by the host: notes may not mention a password", nil
	}
	return true, "", nil
}
