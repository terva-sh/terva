package build

import (
	"fmt"
	"os"

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
func HeadlessConfirmGate(args Args, mode string) (*core.ConfirmGate, *core.ReadOnlySet) {
	pol, warns := BuildPermissionPolicy(args)
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "note: %s\n", w)
	}
	if pol == nil {
		return nil, nil
	}
	switch pol.Mode {
	case core.ApprovalPlan:
		fmt.Fprintf(os.Stderr, "note: approval mode 'plan' in %s mode: read-only tools run, everything else is refused\n", mode)
	case core.ApprovalYolo:
		// Reachable only because rules exist; allow/deny apply
		// silently. ask rules degrade to allow in yolo (yolo never
		// prompts), so only a deny rule refuses here.
	default:
		if mode == "bot" {
			// The bot wires a ChatConfirmer after its loop exists, so
			// confirmation prompts reach the paired chat instead of
			// being refused (see botRun).
			fmt.Fprintf(os.Stderr, "note: approval mode %q in bot mode: tool calls that need confirmation ask the paired user over chat, fail-closed on timeout\n", pol.Mode)
		} else {
			fmt.Fprintf(os.Stderr, "note: approval mode %q in %s mode refuses tool calls that would need confirmation (no interactive prompt available)\n", pol.Mode, mode)
		}
	}
	return core.NewPolicyGate(pol, nil), pol.ReadOnly
}

// ResolveSwarmWorktrees decides whether per-agent swarm worktree
// isolation is on. The --swarm-worktrees flag (flagOverride, non-nil
// when the flag was given) wins over the user config's swarm_worktrees
// (cfg). nil/absent in both means off — today's behavior. Mirrors the
// bool-pointer precedence used for the other swarm/picker settings.
func ResolveSwarmWorktrees(flagOverride, cfg *bool) bool {
	if flagOverride != nil {
		return *flagOverride
	}
	return cfg != nil && *cfg
}
