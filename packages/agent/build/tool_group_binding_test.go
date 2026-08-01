package build

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func newBoundSession(t *testing.T, name string) (*core.Session, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, name)
	sess, err := core.NewSessionAtPath(path, dir, "openai-codex", "gpt-5.6-sol", "test")
	if err != nil {
		t.Fatalf("NewSessionAtPath: %v", err)
	}
	if err := sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	return sess, path
}

// TestWiredPersistenceAndBindingRoundTripAnActivation is the seam test: the
// observer WireHeadlessSessionPersist registers must write a row that
// BindSession reads back. Both halves shipped separately would each look
// correct and together do nothing — which is the shape of the bug being fixed,
// where activation was written nowhere and restored nowhere.
func TestWiredPersistenceAndBindingRoundTripAnActivation(t *testing.T) {
	sess, path := newBoundSession(t, "s.jsonl")

	live := core.NewAgent(nil, "gpt-5.6-sol", "system", core.Registry{})
	live.EnableLazyTools()
	WireHeadlessSessionPersist(live, sess)
	if !live.ActivateGroup("index") {
		t.Fatal("ActivateGroup reported no change")
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, _, err := core.OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer reopened.Close()

	resumed := core.NewAgent(nil, "gpt-5.6-sol", "system", core.Registry{})
	resumed.EnableLazyTools()
	BindSession(SessionBinding{Agent: resumed, Session: reopened})

	got := resumed.ActiveGroups()
	if len(got) != 1 || got[0] != "index" {
		t.Errorf("resumed agent ActiveGroups() = %v; want [index] — a resume that "+
			"drops it re-sends a different tools array and invalidates the whole "+
			"cached transcript", got)
	}
}

// TestBindingASessionWithNoActivationsClearsTheOutgoingSet covers the switch:
// binding fires on resume/fork/new/cd, and the incoming session's activations
// are the whole answer, not an addition to the outgoing session's.
func TestBindingASessionWithNoActivationsClearsTheOutgoingSet(t *testing.T) {
	sessA, pathA := newBoundSession(t, "a.jsonl")
	ag := core.NewAgent(nil, "gpt-5.6-sol", "system", core.Registry{})
	ag.EnableLazyTools()
	WireHeadlessSessionPersist(ag, sessA)
	ag.ActivateGroup("index")
	if err := sessA.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}
	reopenedA, _, err := core.OpenSession(pathA)
	if err != nil {
		t.Fatalf("OpenSession A: %v", err)
	}
	defer reopenedA.Close()
	BindSession(SessionBinding{Agent: ag, Session: reopenedA})
	if got := ag.ActiveGroups(); len(got) != 1 {
		t.Fatalf("precondition: ActiveGroups() = %v; want [index]", got)
	}

	// Switch to a session that never activated anything.
	sessB, pathB := newBoundSession(t, "b.jsonl")
	if err := sessB.Close(); err != nil {
		t.Fatalf("Close B: %v", err)
	}
	reopenedB, _, err := core.OpenSession(pathB)
	if err != nil {
		t.Fatalf("OpenSession B: %v", err)
	}
	defer reopenedB.Close()
	BindSession(SessionBinding{Agent: ag, Session: reopenedB})

	if got := ag.ActiveGroups(); len(got) != 0 {
		t.Errorf("after switching to a session with no activations, ActiveGroups() = %v; "+
			"want empty — a leaked group is advertised by a session with no row for it, "+
			"so its next resume drops it and pays the invalidation again", got)
	}
}

// TestResumingTwiceWritesNoExtraRows guards the append loop: restoring must not
// re-fire the observer, or every resume would grow the file by one row per
// activated group.
func TestResumingTwiceWritesNoExtraRows(t *testing.T) {
	sess, path := newBoundSession(t, "s.jsonl")
	ag := core.NewAgent(nil, "gpt-5.6-sol", "system", core.Registry{})
	ag.EnableLazyTools()
	WireHeadlessSessionPersist(ag, sess)
	ag.ActivateGroup("index")
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i := 0; i < 3; i++ {
		reopened, _, err := core.OpenSession(path)
		if err != nil {
			t.Fatalf("OpenSession %d: %v", i, err)
		}
		resumed := core.NewAgent(nil, "gpt-5.6-sol", "system", core.Registry{})
		resumed.EnableLazyTools()
		WireHeadlessSessionPersist(resumed, reopened)
		BindSession(SessionBinding{Agent: resumed, Session: reopened})
		if got := reopened.ActiveToolGroups; len(got) != 1 {
			t.Fatalf("resume %d: ActiveToolGroups = %v; want exactly [index]", i, got)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
}

// The retired session-search extension is handled by supersession — it is in
// extensions.supersededExtensions, so an installed copy never loads and never
// reaches this merge at all.
//
// This covers what supersession does NOT: superseding keys on the EXTENSION
// name, while the registry collision keys on the TOOL name. A third-party
// extension under any other name may still register a tool called
// session_search, and core's must stay live — built-ins win on conflict, so the
// model keeps the full-fidelity tool rather than a role+text one.
func TestBuiltinSessionSearchWinsAToolNameCollision(t *testing.T) {
	reg := core.Registry{"session_search": &tools.SessionSearchTool{}}
	// nil read-only set — this test is about the registry, not the policy.
	MergeToolsForMode(reg, core.ApprovalAsk, nil, fakeExtSource{names: []string{"session_search"}})

	if _, ok := reg["session_search"].(*tools.SessionSearchTool); !ok {
		t.Fatalf("an extension's session_search shadowed the built-in; the model would get "+
			"the role+text view this tool exists to replace. got %T", reg["session_search"])
	}
}

// An extension tool whose name does NOT collide still merges. Neither
// supersession nor the collision rule may read as "extensions lost their session
// surface": the protocol-3 bridge is unchanged and remains the right one for
// conversation TEXT (clustering, summarisation, export).
func TestASearchExtensionsOtherToolsStillMerge(t *testing.T) {
	reg := core.Registry{"session_search": &tools.SessionSearchTool{}}
	MergeToolsForMode(reg, core.ApprovalAsk, nil, fakeExtSource{names: []string{"session_search", "session_topics"}})

	if _, ok := reg["session_topics"]; !ok {
		t.Error("a non-colliding extension tool was dropped alongside the superseded one")
	}
}
