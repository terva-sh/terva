package build

import (
	"context"
	"fmt"
	"sync/atomic"

	"terva.sh/terva/packages/core"
)

// UntrustedSpawnGateTool is the approval-gate door name for spawning
// sub-agents into an untrusted workspace.
//
// Deliberately NOT "swarm_spawn". That name is a trusted first-party builtin
// (permissions.builtin), so the policy answers VerdictAllow before Check ever
// reaches a confirmer and the prompt would never appear — the door would be
// silently open, which is the failure this whole path exists to close. A
// separate name also gives "allow & remember" the right meaning: keep
// spawning degraded for this session, NOT grant swarm_spawn itself.
//
// It is not registered as a tool and not classified: it is a gate door, not
// something the model can call. The classification census reads the registry,
// so an unregistered door needs no entry there.
const UntrustedSpawnGateTool = "swarm_spawn_untrusted"

// untrustedSpawnSeq mints unique call ids for this door, which sits outside
// the BeforeToolExecute ladder and so has no model tool-call id to borrow.
// Process-wide for the same reason hostGateSeq is: uniqueness is the contract,
// and concurrent sub-agents share the process.
var untrustedSpawnSeq atomic.Uint64

// UntrustedSpawnConfirmer returns swarm_spawn's door onto the approval gate:
// the closure the tool calls when the workspace is untrusted and the model did
// not pass allow_untrusted.
//
// Allowing means "run the sub-agents degraded"; refusing means "I would rather
// fix the trust", and the tool turns that into a model-facing instruction to
// stop retrying. This replaces a flat tool error whose text asked the MODEL to
// relay the problem — which, measured over a 17.5-hour session that hit it five
// times, it never once did.
//
// Returns nil for a nil gate (pure yolo, headless, any mode with no confirmer).
// The tool treats a nil closure as "cannot ask" and keeps the old
// refusal-with-guidance, so a mode with no human attached never blocks a turn
// waiting for one.
func UntrustedSpawnConfirmer(gate *core.ConfirmGate) func(ctx context.Context, preview string) (bool, string) {
	if gate == nil {
		return nil
	}
	return func(ctx context.Context, preview string) (bool, string) {
		callID := fmt.Sprintf("untrusted-spawn-%d", untrustedSpawnSeq.Add(1))
		// nil args: this door gates a CONDITION (an untrusted workspace), not
		// a payload. The spawn's own arguments are not what the user is being
		// asked about, and feeding them to the scope deriver would offer
		// "always allow spawns matching this task", which is not a grant
		// anyone means to make.
		allowed, reason, _ := gate.Check(ctx, UntrustedSpawnGateTool, nil, preview, callID)
		recordGateAudit(auditViaUntrustedSpawn, UntrustedSpawnGateTool, nil, gate, allowed, reason)
		return allowed, reason
	}
}
