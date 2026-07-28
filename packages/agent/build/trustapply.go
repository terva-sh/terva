package build

import (
	"context"
	"time"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/core"
)

// TrustSurfaces is the trust-flip event: everything a host re-derives when the
// Workspace Trust verdict moves, and — in ApplyTrust — the order it happens in.
//
// The fields ARE the list. That is the point of the type: a trust flip has to
// reach four different places, they are easy to enumerate and easy to forget,
// and forgetting one is silent. The ACP host had a /trust verb on its wire that
// moved only the extension manager, so a newly trusted repo's hooks stayed
// inert, its extensions ran where no tool call could reach them, and its lore
// stayed invisible — three separate omissions, each of which looked like
// working code. Nothing named the list, so nothing could say what was missing.
//
// A nil field is nothing to do, not a mistake: the acp session has no broadcast
// to make, and a project with no hooks anywhere builds no engine. But a field
// LEFT OUT of a host's literal is now visible at the call site, which is the
// difference between an omission and a decision.
type TrustSurfaces struct {
	// Args resolves the new verdict's content — the hook specs, and (through
	// Lore) the per-turn context. Leave TrustPin nil: a host applying a trust
	// flip is by definition not a fixed-trust host.
	Args Args

	// Hooks is the engine whose SPECS are swapped for the new verdict. Build it
	// with BuildLiveTrustHookEngine, or an untrusted project with hooks on disk
	// has no engine standing by and this has nowhere to put them.
	Hooks *hooks.Engine

	// Ext is the extension manager whose project-dir gate flips. Its reload is
	// what fires the host's SetOnReload — normally Rebuild — so a host with a
	// manager does not call Rebuild itself.
	Ext   *extensions.Manager
	Grace time.Duration

	// Rebuild re-derives the model's tool set (build.LiveToolSet). Called
	// directly only when there is no Ext to fire it, so a manager-less host
	// still tracks: project skills are gated on the verdict too.
	Rebuild func()

	// Lore re-derives the agent's per-turn context, which is trust-gated the
	// same way skills are. It is a func rather than an *core.Agent because the
	// daemon keeps a lore snapshot of its own beside the agent's provider;
	// RewireLoreContext is the shared half both hosts call.
	Lore func()

	// After is this host's own tail — whatever only it has. The daemon
	// broadcasts a session update so every client re-reads the trust-gated
	// panes; the acp session has no such surface.
	After func()
}

// ApplyTrust brings one host's surfaces in line with a new Workspace Trust
// verdict.
//
// The order is a safety rule, not a style: on a WITHDRAWAL, everything that
// lets the repository RUN CODE has to stop before anything else. Hooks are
// programs the repo runs on every tool call and project extensions are
// subprocesses it runs continuously, so those two go first, in that order.
// Whether the model can SEE the project — its tool set, its lore — follows,
// because being able to see a repo you no longer trust is a lesser problem than
// it still executing.
//
// On a GRANT the order carries no safety weight, and using the same sequence
// leaves one order to reason about rather than two. That symmetry is what the
// daemon was missing: it set the hook specs LAST, after a per-session extension
// reload that can take a couple of seconds, so a tool call landing in that
// window ran an untrusted repo's hook.
func ApplyTrust(ctx context.Context, trusted bool, s TrustSurfaces) {
	// 1. Hooks — nil-safe on the engine, so a project with none needs no case.
	s.Hooks.SetSpecs(HookSpecsFor(s.Args, trusted))

	// 2. The extension gate, then the reload that starts or stops the
	//    subprocesses. The reload fires the host's SetOnReload, which is how
	//    Rebuild runs for a host that has a manager.
	switch {
	case s.Ext != nil:
		s.Ext.SetProjectTrusted(trusted)
		s.Ext.Reload(ctx, s.Grace)
	case s.Rebuild != nil:
		s.Rebuild()
	}

	// 3. What the model sees.
	if s.Lore != nil {
		s.Lore()
	}
	// 4. Whatever only this host has.
	if s.After != nil {
		s.After()
	}
}

// RewireLoreContext re-derives ag's per-turn lore context from a fresh Resolve
// and swaps it onto the running agent, along with its side-effect-free peek
// twin so a context-size view reflects the same set.
//
// Lore is trust-gated (lore.Discover takes the verdict), and every agent gets
// its provider once at NewAgent — so without this a trust flip leaves the
// launch answer in place and a newly trusted project's keyed entries never
// fire. Constant lore is a different matter: it is baked into the system prompt
// at resolve time and lands on the next session, like skills and context files.
//
// Best-effort: a Resolve that fails (no credential, in a test) leaves the
// current provider standing rather than clearing it, and reports nil.
//
// The resolve is handed back because it is the expensive part and a caller
// usually has more to re-point at it — the daemon re-seeds its per-turn tail
// records from the same one rather than paying for a second.
func RewireLoreContext(ag *core.Agent, args Args) *Resolved {
	if ag == nil {
		return nil
	}
	r, err := Resolve(args, true)
	if err != nil {
		return nil
	}
	ag.SetContextProvider(r.PerTurnContext(ag))
	ag.SetContextProviderPeek(r.PerTurnContextPeek(ag))
	return &r
}
