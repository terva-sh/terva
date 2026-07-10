package modes

// The shell escape's slot exit must not touch the daemon's queue.
//
// In carrier mode the Workspace owns post-turn queue policy — wsSession.endTurn
// shifts the head on success, drains on failure, and broadcasts queue_updated
// so every client converges. releaseCarrier has always deferred to that. The
// shell escape's exit, release(dropQueue), did not: it carried the legacy
// path's queue policy, from back when the engine's agent was the TUI's own.
//
// It no longer was. cli_ctrlproto handed the DAEMON's session agent to the TUI,
// so `turns.agent.DrainQueuedMessages()` reached across into shared state.
// Measured against the code as it stood:
//
//	release(true)  // esc: queue 2 -> 0, no broadcast; other clients still
//	               // render chips for prompts that no longer exist
//	release(false) // exit 0: pops the head and re-dispatches it locally,
//	               // racing endTurn's shift for the same message
//
// The fix makes it structural rather than behavioural: the engine keeps no
// agent, so there is no queue for the TUI to reach. TestModesNeverMutatesTheDaemonQueue
// is the guard that keeps it that way — the behavioural half now belongs to the
// workspace, which is the only thing that owns a queue.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// --- the slot still does its own job -------------------------------------

// The escape claims a slot so prompts are gated and esc has a target. Removing
// the queue policy must not weaken either.
func TestShellEscapeSlotGatesPromptsAndReleases(t *testing.T) {
	e := newTestEngine()

	if !e.claimSlot(func() {}) {
		t.Fatal("first claim must win on an idle engine")
	}
	if e.claimCarrier(func() {}) {
		t.Fatal("a prompt was dispatched while a shell escape held the slot")
	}
	if e.claimCompact(func() {}, false) {
		t.Fatal("a compaction ran while a shell escape held the slot")
	}

	e.releaseSlot()

	if e.Busy() {
		t.Fatal("releaseSlot left the slot held")
	}
	if !e.claimCarrier(func() {}) {
		t.Fatal("the slot was never released")
	}
}

// esc during a shell escape cancels the command — the slot's own cancel func —
// and releaseSlot drops it, so a later esc cannot reach a dead command.
func TestShellEscapeSlotCancelIsLocalAndCleared(t *testing.T) {
	e := newTestEngine()
	cancelled := make(chan struct{})
	if !e.claimSlot(func() { close(cancelled) }) {
		t.Fatal("harness: claim failed on an idle engine")
	}

	if !e.cancelActive() {
		t.Fatal("esc did not reach the shell command")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the shell command's cancel func never ran")
	}

	e.releaseSlot()
	if e.cancelActive() {
		t.Fatal("the cancel func outlived the slot")
	}
}

// A compaction's slot exit is releaseCarrier's, not the escape's; pinned here
// because releaseSlot clears autoCompacting too and the two must not diverge.
func TestReleaseSlotClearsTheCompactFlag(t *testing.T) {
	e := newTestEngine()
	if !e.claimCompact(func() {}, true) {
		t.Fatal("compact claim failed on an idle engine")
	}
	if !e.AutoCompacting() {
		t.Fatal("autoCompacting not set")
	}
	e.releaseSlot()
	if e.AutoCompacting() {
		t.Fatal("autoCompacting survived releaseSlot")
	}
}

// --- the structural guard --------------------------------------------------

// queueMutators are core.Agent's queue methods. The daemon calls these — it owns
// the queue. A TUI that calls one is reaching across a process boundary that
// only happens to be absent today, and mutating state every other client
// mirrors, without broadcasting the change.
//
// Reads are here too, deliberately. The TUI renders its queue from the
// queue_updated mirror (see Interactive.carrierQueued); a direct read would be a
// second, silently-diverging source of truth.
var queueMutators = []string{
	"QueueMessage",
	"DrainQueuedMessages",
	"ShiftQueuedMessage",
	"RequeueFront",
	"SetQueuedMessages",
	"QueuedMessageCount",
	"PendingQueuedMessages",
}

// agentQueueCallSites parses this package's non-test sources and reports every
// call to a queue method by selector name (x.QueueMessage(...)). Parsed, not
// grepped, so a mention in a comment or a string cannot trip it — the doc
// comments in this package name these methods on purpose.
func agentQueueCallSites(t *testing.T) map[string][]string {
	t.Helper()
	forbidden := map[string]bool{}
	for _, m := range queueMutators {
		forbidden[m] = true
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	sites := map[string][]string{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !forbidden[sel.Sel.Name] {
					return true
				}
				sites[sel.Sel.Name] = append(sites[sel.Sel.Name], filepath.Base(name))
				return true
			})
		}
	}
	return sites
}

// TestModesNeverMutatesTheDaemonQueue: the queue belongs to the Workspace. The
// TUI dispatches onto it with the Queue / SetQueue verbs and renders it from
// the queue_updated mirror; it never calls the agent's queue methods.
func TestModesNeverMutatesTheDaemonQueue(t *testing.T) {
	for method, files := range agentQueueCallSites(t) {
		sort.Strings(files)
		t.Errorf("%s called from %s — the daemon owns the queue. "+
			"Dispatch through Carrier.Queue / Carrier.SetQueue (which broadcast "+
			"queue_updated) and render from the carrierQueued mirror.",
			method, strings.Join(files, ", "))
	}
}

// The probe's own instrument check: the AST walk must actually SEE a selector
// call of a forbidden name. Without this, a broken parser (wrong directory, a
// changed AST shape) reports a clean package forever.
func TestAgentQueueProbeSeesACall(t *testing.T) {
	src := `package p
func f(a A) { a.DrainQueuedMessages(); a.Fine() }`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var seen []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			seen = append(seen, sel.Sel.Name)
		}
		return true
	})
	if len(seen) != 2 || seen[0] != "DrainQueuedMessages" {
		t.Fatalf("the AST probe does not see selector calls it must catch: %v", seen)
	}
}
