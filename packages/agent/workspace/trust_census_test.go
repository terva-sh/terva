package workspace

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Workspace Trust took three fixes to become live everywhere it needed to be
// (the /trust-takes-effect-immediately work): the verdict has three storage
// forms, and a reader that captures the launch-time snapshot — build.Resolved's
// Trusted field, or a bool copied at construction — goes stale the moment the
// user flips trust, silently. Nothing stopped the NEXT reader from doing
// exactly that.
//
// This census parses the packages that consume trust and requires every
// `.Trusted` FIELD READ (method calls like w.Trusted() are the live accessor
// and need no entry) to be classified below: live (an atomic or a re-resolve),
// wire data (a view field a client renders, refreshed by fetch), or a
// launch-time snapshot with the reason it is acceptable — and, where one
// exists, the live twin that re-runs the decision later. A new unclassified
// read fails; a classified read that disappears fails the other way.
//
// This is the STATIC half. It can tell that a read exists and that someone
// classified it; it cannot tell whether a claimed live twin is real, because a
// reader that captures the launch answer and one that re-resolves look the same
// to a parser. The behavioural halves live with their hosts:
// workspace_trust_live_sweep_test.go flips trust on a live workspace and asserts
// what actually moves, and ../acp_trust_live_test.go does the same for the ACP
// session — the host that had a trust verb on its wire and no live twin behind
// it, so /trust persisted the verdict and the open session went on running the
// launch answer.
//
// Key format: "<dir>/<file>:<receiver>.Trusted" → occurrence count.
type trustReads struct {
	count int
	note  string
}

var trustCensus = map[string]trustReads{
	// --- live readers ---
	"workspace_session.go:w.Trusted":    {1, "live: the METHOD VALUE handed to SwarmSpawnTool.Trusted — the spawn gate reads through it at call time"},
	"../tools/swarm_spawn.go:t.Trusted": {1, "live: nil-check of the closure field the workspace wires; the verdict itself comes from calling it"},
	"worktree_provenance.go:p.Trusted":  {6, "live-reresolve at capture, then DELIBERATELY frozen: worktreeTrustVerdict re-reads the store on every lease, and the record it produces is retained in Workspace.swarmBatch for the note (renderSwarmWorktreeNote's restricted count, swarmWorktreeGrantHint's/swarmWorktreeAllGranted's lease-time side, the TrustedNow seed). The frozen reads must NOT re-resolve — the record states the posture each sub-agent BOOTED with, and a `terva trust` granted afterwards does not retroactively give a child extensions it never loaded. The live twin is the TrustedNow field (not counted here — different selector): re-probed against the current store by refreshSwarmWorktreeTrust via the workspace's trust-store watcher, it drives ONLY the grant hint and its acknowledgment, which closed the old stale-advice cost this entry used to carry"},
	// --- wire data (a view field, refreshed by fetch/broadcast) ---
	"workspace.go:info.Trusted":                    {1, "wire-data producer: SessionInfo.Trusted written from the live per-session atomic"},
	"../modes/interactive_ctrlproto.go:pv.Trusted": {1, "wire-data: PermissionsView rendered by the permissions pane, re-fetched on surface_updated"},
	"../cli_ctrlproto.go:info.Trusted":             {1, "boot snapshot from SessionInfo, handed to InteractiveConfig; the three TUI writers keep it live afterwards"},
	"../attach_mode.go:cur.Trusted":                {1, "boot snapshot from SessionInfo for the attach TUI's InteractiveConfig; same liveness contract as above"},
	// --- the TUI's hand-live bool: moved by exactly three writers (/trust,
	// /untrust, the settings toggle). New READERS are fine; a new COPY into
	// another struct at construction is the trap this census exists to catch.
	"../modes/interactive.go:i.cfg.Trusted":          {1, "TUI hand-live bool, read per use"},
	"../modes/interactive_settings.go:i.cfg.Trusted": {1, "TUI hand-live bool: the settings-toggle writer"},
	"../modes/slash_registry.go:i.cfg.Trusted":       {2, "TUI hand-live bool: the /trust and /untrust writers"},
	"../modes/status_view.go:i.cfg.Trusted":          {1, "TUI hand-live bool copied into the footer struct — rebuilt every render, so it tracks"},
	"../modes/status_view.go:f.Trusted":              {1, "per-render footer copy, see above"},
	// --- store data, not a verdict ---
	"../trustcmd.go:s.Trusted": {2, "the trust STORE's entry list (trusted directories), not a session verdict"},
	// --- launch-time snapshots, each with its reason ---
	"workspace.go:r.Trusted":         {2, "launch seed of the live atomic, plus the hook engine's seed — live since 2026-07-27: applyTrust re-merges the trust-gated project hooks and swaps the engine's SPECS in place (build.HookSpecsFor + Engine.SetSpecs), so the pointer is stable while the chain is current. Behaviourally covered by TestATrustFlipStartsAndStopsProjectHooks"},
	"workspace_session.go:r.Trusted": {4, "session-build snapshots with live twins: setTrusted re-runs lore discovery and SetProjectTrusted; the cast re-derives on rebuild"},
	"../build/wiring.go:r.Trusted":   {2, "the hook/MCP merge implementations are snapshot-by-construction; liveness is their CALLERS' contract, classified per host here"},
	"../rpc.go:r.Trusted":            {4, "rpc has no trust verb at all — the posture is fixed for the process's lifetime by design, and since 2026-07-28 provably so: the extension-manager and hook-engine seeds, plus the TrustPin the tool rebuild resolves against, so the one path that re-runs Resolve cannot read a store that moved under it. Behaviourally covered by build's TestATrustPinOverridesTheStoreInBothDirections"},
	"../cli.go:r.Trusted":            {6, "one-shot processes: launch is the lifetime"},
	"../swarm_agent.go:r.Trusted":    {2, "one-shot child process: launch is the lifetime"},
	// --- live readers in another package ---
	"../acp_mode.go:r.Trusted": {5, "live since 2026-07-28: ACP carries /trust and /untrust on the wire, and acpApplyTrust re-derives both halves on a flip — the hook engine's SPECS (BuildLiveTrustHookEngine + HookSpecsFor, the workspace's own mechanism) and the model's tool set (build.LiveToolSet). These are the launch SEEDS of state that is now re-resolved. Behaviourally covered by TestAnACPTrustFlipStartsAndStopsProjectHooks and TestAnACPTrustFlipPutsAProjectSkillInReachOfTheModel"},
}

func TestEveryTrustReadIsClassified(t *testing.T) {
	dirs := []string{".", "..", "../build", "../modes", "../tools"}
	found := map[string]int{}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			calls := map[ast.Expr]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					calls[c.Fun] = true
				}
				return true
			})
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Trusted" || calls[sel] {
					return true
				}
				var b bytes.Buffer
				_ = printer.Fprint(&b, fset, sel.X)
				key := filepath.ToSlash(filepath.Join(dir, name)) + ":" + b.String() + ".Trusted"
				found[key]++
				return true
			})
		}
	}
	if total := len(found); total < 5 {
		t.Fatalf("census saw only %d distinct trust reads; the walk is not seeing them", total)
	}

	var problems []string
	for key, n := range found {
		want, ok := trustCensus[key]
		switch {
		case !ok:
			problems = append(problems, key+" ("+itoa(n)+" reads) is a new trust FIELD read — classify it (live / wire-data / launch-snapshot+reason)")
		case want.count != n:
			problems = append(problems, key+": census says "+itoa(want.count)+" reads, source has "+itoa(n)+" — re-audit and update")
		}
	}
	for key := range trustCensus {
		if _, ok := found[key]; !ok {
			problems = append(problems, key+" is in the census but no longer in the source — delete the entry")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("trust census out of date:\n  %s", strings.Join(problems, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
