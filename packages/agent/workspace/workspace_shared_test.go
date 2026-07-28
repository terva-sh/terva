package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// shareTestWorkspace is chatTestWorkspace plus a real share store, since what is
// under test is publication into one.
func shareTestWorkspace(t *testing.T, id string) (*Workspace, *wsSession, *attach.Store) {
	t.Helper()
	w, s, _ := chatTestWorkspace(t, id)
	store := attach.NewShareStoreAt(testsupport.TempDir(t))
	w.shared = store
	return w, s, store
}

// deriveShare runs the same derivation an extension reload triggers and returns
// the tool it produced.
func deriveShare(t *testing.T, w *Workspace, s *wsSession, args build.Args) core.Tool {
	t.Helper()
	r := build.Resolved{ToolRegistry: core.Registry{}, CWD: s.cwd}
	w.injectExtraTools(s, &r, args)
	return r.ToolRegistry["share_file"]
}

// The end-to-end claim, at the seam where it is decided: the tool the workspace
// registers publishes into THIS session's share area, and describes what landed
// rather than what it was told.
func TestShareFileToolPublishesIntoTheSession(t *testing.T) {
	w, s, store := shareTestWorkspace(t, "s1")
	src := filepath.Join(testsupport.TempDir(t), "report.pdf")
	if err := os.WriteFile(src, []byte("%PDF-1.4 body"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := deriveShare(t, w, s, build.Args{})
	if tool == nil {
		t.Fatal("share_file was not registered for a base workspace session")
	}
	res, err := tool.Execute(context.Background(), []byte(`{"path":`+quote(src)+`}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Shared) != 1 {
		t.Fatalf("Shared = %+v, want one record", res.Shared)
	}

	// The record describes what is on disk, in this session's dir.
	ref, err := store.Resolve("s1", res.Shared[0].ID)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", res.Shared[0].ID, err)
	}
	if got, err := os.ReadFile(ref.Path); err != nil || string(got) != "%PDF-1.4 body" {
		t.Fatalf("published bytes = %q, %v; want the source body", got, err)
	}
	if res.Shared[0].Name != "report.pdf" || res.Shared[0].Kind != "document" {
		t.Errorf("record = %+v, want report.pdf as a document", res.Shared[0])
	}
	if !strings.HasPrefix(ref.Path, store.SessionDir("s1")) {
		t.Errorf("published to %q, want it under s1's dir", ref.Path)
	}
}

// The publisher's session is bound at registration, so a second session's tool
// cannot land a file in the first's conversation even if it wanted to.
func TestShareFileToolCannotPublishIntoAnotherSession(t *testing.T) {
	w, s, store := shareTestWorkspace(t, "s1")
	other := newTestSession()
	other.id, other.ws, other.cwd = "s2", w, s.cwd
	src := filepath.Join(testsupport.TempDir(t), "notes.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := deriveShare(t, w, other, build.Args{})
	res, err := tool.Execute(context.Background(), []byte(`{"path":`+quote(src)+`}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := store.Resolve("s1", res.Shared[0].ID); err == nil {
		t.Error("s2's share resolved in s1's session")
	}
	if _, err := store.Resolve("s2", res.Shared[0].ID); err != nil {
		t.Errorf("s2's share did not resolve in its own session: %v", err)
	}
}

// The skin gate: --chat and --play sessions have no filesystem tools to produce
// a file with, and an immersive scene is not a place to hand over a download.
// Same rule raati_convene follows.
func TestShareFileToolIsWithheldFromImmersiveSessions(t *testing.T) {
	w, s, _ := shareTestWorkspace(t, "s1")

	for _, tc := range []struct {
		name string
		args build.Args
	}{
		{"play", build.Args{Experience: "play"}},
		{"no-tools", build.Args{NoTools: true}},
		{"no-workspace-tools", build.Args{NoWorkspaceTools: true}},
	} {
		if tool := deriveShare(t, w, s, tc.args); tool != nil {
			t.Errorf("%s session was given share_file", tc.name)
		}
	}
}

// quote is a minimal JSON string literal for a filesystem path (Windows
// separators would otherwise land as escapes).
func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// The deadline has to survive the whole path — store, publisher, tool result,
// wire, and the session file — because the surface it exists for is a
// transcript reopened long after the turn, which is exactly the read that goes
// through all of it.
func TestSharedRecordCarriesTheExpiryToTheWire(t *testing.T) {
	w, s, store := shareTestWorkspace(t, "s1")
	src := filepath.Join(testsupport.TempDir(t), "report.pdf")
	if err := os.WriteFile(src, []byte("%PDF-1.4 body"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := deriveShare(t, w, s, build.Args{})
	res, err := tool.Execute(context.Background(), []byte(`{"path":`+quote(src)+`}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := res.Shared[0].ExpiresAt
	if got == "" {
		t.Fatal("the record carries no expiry — every non-image card keeps offering a dead download")
	}
	at, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("expires_at %q is not RFC3339: %v", got, err)
	}

	// The same instant the store computed, which is the same one the sweeper
	// will act on. A record that merely LOOKS like a timestamp is not the claim.
	ref, err := store.Resolve("s1", res.Shared[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Equal(ref.ExpiresAt.Truncate(time.Second)) {
		t.Errorf("wire expiry %v != the store's %v", at, ref.ExpiresAt)
	}
	// Far enough out to be the OUTBOUND policy: a share outlives an upload
	// sevenfold, and quoting the inbound TTL here would silently halve a
	// deliverable's advertised life.
	if d := time.Until(at); d < attach.ShareTTL-time.Minute {
		t.Errorf("expiry is %v away, want ~%v — this looks like the inbound TTL", d, attach.ShareTTL)
	}
}

// An ordinary tool result grows nothing. Every field on the record is one a
// client may eventually key off, and a turn that shared nothing has no answer
// to give about expiry.
func TestNonSharingToolResultCarriesNoExpiry(t *testing.T) {
	w, s, _ := shareTestWorkspace(t, "s1")
	tool := deriveShare(t, w, s, build.Args{})
	res, err := tool.Execute(context.Background(), []byte(`{"path":"/nonexistent/nope.txt"}`), nil)
	if err == nil && len(res.Shared) > 0 {
		t.Fatalf("a failed share produced a record: %+v", res.Shared)
	}
}
