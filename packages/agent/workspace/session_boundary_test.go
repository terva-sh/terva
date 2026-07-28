package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A wsSession reaches its Workspace through the ws back-pointer. That pointer is
// the whole *Workspace, so nothing but habit stops a session verb from taking the
// registry's lock or walking its session map — and a session that does either can
// deadlock against resolve(), which holds mu while it builds a session that is
// itself running session code.
//
// Today the habit holds perfectly: s.ws.mu and s.ws.sessions appear ZERO times in
// the package. This test is that convention written down. It parses every s.ws.X
// out of the non-test sources and requires X to be on the list below with a reason,
// so a new reach has to be argued for rather than merely compiled.
//
// The list is the contract doc 13 §2.1 says is missing: "what a session legitimately
// needs from its workspace." When it is eventually replaced by a narrow interface
// (13 §3, tier 2), this test becomes that interface's completeness check rather than
// being deleted — the members named here are its method set.
//
// A stale entry fails too: an allowance nobody uses is a claim about the code that
// has stopped being true.
var sessionMayReachWorkspace = map[string]string{
	// --- daemon lifetime ---
	"ctx": "the daemon's context — a session's background work must die with the workspace, not outlive it",

	// --- workspace-scoped fan-out ---
	"BroadcastAll": "workspace-scoped events (sessions-changed, surface updates) belong to the hub, not to one session's stream",

	// --- workspace-global singletons a session's agent is built against ---
	"mcpAdapter": "MCP servers are started once per daemon (subprocesses are expensive to respawn per session) and merged into every session's agent",
	"mcpMu":      "serializes live MCP toggles; the adapter above is shared, so the lock guarding it must be too",
	"swarm":      "background sub-agents are workspace-global (the tasks pane), not per-session",
	"sandbox":    "the filesystem sandbox is a workspace-scoped posture — /jail confines every session at once",
	"diag":       "host-side diagnostics sink; the TUI overrides it so a stray stderr write cannot corrupt the alternate screen",

	// --- content stores, keyed by workspace paths ---
	"cardStore": "the character-card library lives at a workspace path, not a session one",
	"cardName":  "resolves a card id to its display name out of that same library",

	// --- cross-session operations a session verb legitimately triggers ---
	// Each of these applies a change made in ONE session to every open session.
	// They live on the workspace because that is the only side that can see them all.
	"refreshAllPolicies":    "a permission change made in one session applies to every open session",
	"rebuildAllSessions":    "an extension/MCP reload rebuilds every session's agent, not just the caller's",
	"applyReasoning":        "a reasoning-effort change goes live to every session's agent, not only the settings pane's",
	"applyReasoningSummary": "same, for the reasoning-summary setting",
	"applyEngineFeature":    "same, for an engine feature toggle out of the settings pane",
	"applyAutoSwarm":        "auto-swarm config is resolved once for the workspace and applied to the spawning session",
	"injectExtraTools":      "the spawn gate reads workspace trust, which is workspace-scoped and moves live",
	"overrideClient":        "model override resolves against the workspace's provider/credential set",
	"titleGen":              "title generation uses the workspace default model, not the session's switched one",
	"switchModel":           "a model switch is routed through the workspace so favorites/defaults stay one code path",
	"Trust":                 "trust verdicts are workspace-scoped by definition; a session verb only forwards",
	"Untrust":               "the symmetric inverse of Trust, same reason",

	// --- workspace-scoped panes a session's surface renderer displays ---
	// The surfaces renderer is reached through a session (it renders that session's
	// pane list) but several panes describe the DAEMON, so their data comes from
	// the workspace side.
	"hasTasks":           "the tasks pane is workspace-global; a session asks whether it has anything to show",
	"taskList":           "reads that same workspace-global task board",
	"taskAction":         "acts on it",
	"raatiView":          "the deliberation board is workspace-scoped (at most one live deliberation)",
	"raatiAction":        "acts on that same board",
	"chatView":           "the chat-bridge registry is workspace-scoped; bridges are bound to a session id but owned by the workspace",
	"chatAction":         "acts on that same registry, passing the session id rather than reading it locally",
	"chatStopForSession": "tears down this session's bridges out of that workspace-owned registry",
	"AuthProviders":      "the providers pane reports DAEMON credential state — a credential belongs to the workspace, not a conversation",
	"canLogin":           "whether that pane may render login controls at all, which only the workspace knows (EnableAuth)",
}

// The two members that must NEVER appear. They are not merely undeclared — they are
// the reason this test exists, so name them and say what goes wrong, rather than
// letting them fail with the generic "not on the list" message.
var sessionMustNotReach = map[string]string{
	"mu":       "resolve() holds mu while it builds a session; a session verb taking it can deadlock against its own materialization",
	"sessions": "the registry's map is the workspace's to own — a session walking it can observe a half-built peer",
}

// workspaceMembersReachedFromSession returns every X in an `<expr>.ws.X` selector
// across the package's non-test sources, mapped to the files that reach it.
func workspaceMembersReachedFromSession(t *testing.T) map[string][]string {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	out := map[string][]string{}
	scanned := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			// the shape is <outer>.Sel where <outer> is <recv>.ws
			outer, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			inner, ok := outer.X.(*ast.SelectorExpr)
			if !ok || inner.Sel.Name != "ws" {
				return true
			}
			member := outer.Sel.Name
			for _, seen := range out[member] {
				if seen == path {
					return true
				}
			}
			out[member] = append(out[member], path)
			return true
		})
	}
	// A glob that quietly matched nothing would make every assertion below vacuous.
	if scanned < 10 {
		t.Fatalf("scanned only %d non-test files; the glob is not seeing the package", scanned)
	}
	return out
}

// Every member a session reaches on its workspace must be declared, with a reason.
func TestSessionReachesOnlyDeclaredWorkspaceMembers(t *testing.T) {
	reached := workspaceMembersReachedFromSession(t)

	var undeclared []string
	for member, files := range reached {
		if _, ok := sessionMayReachWorkspace[member]; ok {
			continue
		}
		if why, banned := sessionMustNotReach[member]; banned {
			t.Errorf("a session reaches s.ws.%s in %s — %s", member, strings.Join(files, ", "), why)
			continue
		}
		undeclared = append(undeclared, member+" ("+strings.Join(files, ", ")+")")
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("a session reaches workspace members that are not declared in "+
			"sessionMayReachWorkspace. Add each with a reason saying what a session "+
			"legitimately needs it FOR — or move the work to the workspace side:\n  %s",
			strings.Join(undeclared, "\n  "))
	}
}

// An allowance nobody uses is a claim about the code that has stopped being true.
// Without this, the list decays into a wish list and the test above weakens every
// time a reach is removed.
func TestNoStaleWorkspaceReachAllowances(t *testing.T) {
	reached := workspaceMembersReachedFromSession(t)
	var stale []string
	for member := range sessionMayReachWorkspace {
		if _, ok := reached[member]; !ok {
			stale = append(stale, member)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("declared in sessionMayReachWorkspace but no longer reached — delete "+
			"these entries: %v", stale)
	}
}

// The registry's lock and map are the workspace's alone. This states it as its own
// assertion so the failure names the hazard rather than reading as one more
// undeclared member.
func TestSessionNeverReachesTheRegistry(t *testing.T) {
	reached := workspaceMembersReachedFromSession(t)
	for member, why := range sessionMustNotReach {
		if files, ok := reached[member]; ok {
			t.Errorf("s.ws.%s reached from %s — %s", member, strings.Join(files, ", "), why)
		}
	}
}
