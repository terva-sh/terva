package permissions

import (
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/core"
)

// Agent-construction pieces that read Args: the headless confirm gate and
// the swarm-worktree flag resolution. Hosts (cli, rpc, bot, swarm-agent)
// call these to build an agent; they are not CLI concerns.

// HeadlessConfirmGate returns the confirmation gate for a headless
// mode (print / json / rpc / swarm-agent), or nil when the policy is
// pure yolo (no rules, no mode override) — the historical no-gate fast
// path. There is no interactive prompt in these modes, so the gate is
// constructed with a nil inner Confirmer: a call the policy says to
// *ask* about is refused with a model-readable reason (see
// core.ConfirmGate.Check) instead of running unconfirmed — the
// refuse-by-default posture. Policy allow/deny rules and the mode's
// auto-allows still apply, so headless automation can run a curated
// tool set (e.g. plan mode permits the read-only tools). A one-line
// stderr note tells the human what stance is active; the actual gating
// happens in the BeforeToolExecute closure that calls gate.Check first.
// The second return is the policy's read-only registry, to hand to
// Resolved.AdoptReadOnlySet so read_only-annotated extension/MCP
// tools join the classification. Nil alongside a nil gate.
//
// The run mode is read from args rather than passed alongside it. It used to be
// a second `mode string` parameter, which meant every caller spelled the mode
// twice — once typed in Args.Mode, once as a bare literal — with nothing
// requiring the two to agree. All five callers did agree, but a test did not:
// it passed "json" against an Args carrying no Mode at all, and the
// disagreement was invisible because only the string reached the message.
func HeadlessConfirmGate(p Inputs) (*core.ConfirmGate, *core.ReadOnlySet) {
	pol, warns := BuildPolicy(p)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "note: %s\n", w)
	}
	if pol == nil {
		return nil, nil
	}
	runMode := string(p.Mode)
	switch pol.Mode {
	case core.ApprovalPlan:
		fmt.Fprintf(os.Stderr, "note: approval mode 'plan' in %s mode: read-only tools run, everything else is refused\n", runMode)
	case core.ApprovalYolo:
		// Reachable only because rules exist; allow/deny apply
		// silently. ask rules degrade to allow in yolo (yolo never
		// prompts), so only a deny rule refuses here.
	default:
		if p.Mode == mode.Bot {
			// The bot wires a ChatConfirmer after its loop exists, so
			// confirmation prompts reach the paired chat instead of
			// being refused (see botRun).
			fmt.Fprintf(os.Stderr, "note: approval mode %q in bot mode: tool calls that need confirmation ask the paired user over chat, fail-closed on timeout\n", pol.Mode)
		} else if p.Mode == mode.RPC {
			// RPC can carry approvals out-of-band, so the refusal is opt-out:
			// name the flags that fill the gate instead of leaving a dead end.
			fmt.Fprintf(os.Stderr, "note: approval mode %q in rpc mode refuses tool calls that would need confirmation (no interactive prompt available); carry approvals over the wire with --rpc-approvals, or out-of-band with --approval-socket / --approval-http (see docs/rpc.md)\n", pol.Mode)
		} else {
			fmt.Fprintf(os.Stderr, "note: approval mode %q in %s mode refuses tool calls that would need confirmation (no interactive prompt available)\n", pol.Mode, runMode)
		}
	}
	return core.NewPolicyGate(pol, nil), pol.ReadOnly
}
