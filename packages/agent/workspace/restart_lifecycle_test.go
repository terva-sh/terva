package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/restartmarker"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seedSession writes a non-empty session on disk (so it survives the boot-time
// empty-session prune) and returns its wire id. When interrupted is true it
// ends on an assistant tool_use with no result — the shape a killed tool call
// leaves — so the reconciliation path has something to repair.
func seedSession(t *testing.T, home, cwd string, interrupted bool) string {
	t.Helper()
	s, err := core.NewSession(home, cwd, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "apply the unit change and restart"}}}); err != nil {
		t.Fatal(err)
	}
	if interrupted {
		if err := s.AppendMessage(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: "call-1", Name: "bash", Arguments: []byte(`{"cmd":"systemctl --user restart terva"}`)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return build.SessionIDFromPath(s.Path)
}

// TestPlannedRestartResumesAndReconciles exercises slices 1+2 end to end: a
// planned-restart marker steers the empty-id default to the exact session that
// was live (over latest-on-disk), and that session's interrupted tool call is
// reconciled as a NON-error planned restart rather than a generic failure.
func TestPlannedRestartResumesAndReconciles(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	// Two sessions: the marked one (with an interrupted call) and a LATER,
	// unrelated one that would otherwise win the latest-on-disk default.
	marked := seedSession(t, home, cwd, true)
	latest := seedSession(t, home, cwd, false)
	if marked == latest {
		t.Fatal("seeded ids collided")
	}
	// Pin mtimes so `latest` is unambiguously the most recent on disk — the
	// marker must override that, not merely agree with it.
	base := time.Now()
	setMtime(t, home, cwd, marked, base.Add(-2*time.Minute))
	setMtime(t, home, cwd, latest, base.Add(-1*time.Minute))

	// Arm a planned restart for the marked (older) session.
	if err := restartmarker.Arm(home, restartmarker.Marker{
		Session:     marked,
		FromVersion: "0.126.0",
		Reason:      "apply unit change",
		ExpiresUnix: base.Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "0.126.9")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Sanity: without the marker the empty-id default would pick `latest`.
	if got := build.SessionIDFromPath(core.LatestSession(home, cwd)); got != latest {
		t.Fatalf("precondition: latest-on-disk = %q, want %q", got, latest)
	}

	// The marker steers the empty-id default to the exact live session.
	s, err := w.resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if s.id != marked {
		t.Fatalf("resolve(\"\") = %q, want the marked session %q (marker did not override latest)", s.id, marked)
	}

	// The interrupted tool call is reconciled as a NON-error planned restart.
	if !hasPlannedReconcile(s.agent.Messages()) {
		t.Errorf("interrupted call not reconciled as a planned restart:\n%s", dumpToolResults(s.agent.Messages()))
	}

	// The marker is one-shot: consumed off disk at boot.
	if _, err := os.Stat(restartmarker.Path(home)); !os.IsNotExist(err) {
		t.Error("marker still on disk after recovery")
	}
}

// TestPlannedRestartMarkedSessionGoneFallsBack: if the marked session no longer
// exists, empty-id resolution falls back to latest-on-disk rather than erroring
// or resuming nothing.
func TestPlannedRestartMarkedSessionGoneFallsBack(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	latest := seedSession(t, home, cwd, false)
	if err := restartmarker.Arm(home, restartmarker.Marker{
		Session:     "a-session-that-was-deleted",
		ExpiresUnix: time.Now().Add(time.Hour).Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	s, err := w.resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if s.id != latest {
		t.Errorf("resolve(\"\") = %q, want fallback to latest %q", s.id, latest)
	}
}

// TestRecoveredRestartNoticeShape pins the typed recovered event's kind and
// data keys (the machine-readable contract a fleet control plane consumes).
func TestRecoveredRestartNoticeShape(t *testing.T) {
	w := &Workspace{version: "0.126.9"}
	m := &restartmarker.Marker{Session: "sid", FromVersion: "0.126.0", Reason: "apply unit change"}
	e := w.recoveredRestartNotice(m, "sid")
	if e == nil || e.Notice == nil {
		t.Fatal("nil recovered notice")
	}
	if e.Notice.Kind != ctrlproto.NoticeRestart {
		t.Errorf("kind = %q, want %q", e.Notice.Kind, ctrlproto.NoticeRestart)
	}
	for k, want := range map[string]string{"phase": "recovered", "from_version": "0.126.0", "to_version": "0.126.9", "session": "sid"} {
		if got := e.Notice.Data[k]; got != want {
			t.Errorf("data[%q] = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(e.Notice.Text, "apply unit change") {
		t.Errorf("notice text %q omits the reason", e.Notice.Text)
	}
}

func setMtime(t *testing.T, home, cwd, id string, when time.Time) {
	t.Helper()
	p := filepath.Join(core.SessionsDir(home, cwd), id+".jsonl")
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func hasPlannedReconcile(msgs []provider.Message) bool {
	for _, m := range msgs {
		if m.Role != provider.RoleTool {
			continue
		}
		for _, c := range m.Content {
			tr, ok := c.(provider.ToolResultBlock)
			if !ok {
				continue
			}
			tb, ok := tr.Content[0].(provider.TextBlock)
			if !ok {
				continue
			}
			if !tr.IsError && strings.Contains(strings.ToLower(tb.Text), "planned restart") {
				return true
			}
		}
	}
	return false
}

func dumpToolResults(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role != provider.RoleTool {
			continue
		}
		for _, c := range m.Content {
			tr, ok := c.(provider.ToolResultBlock)
			if !ok {
				continue
			}
			text := ""
			if len(tr.Content) > 0 {
				if tb, ok := tr.Content[0].(provider.TextBlock); ok {
					text = tb.Text
				}
			}
			b.WriteString("  [isError=")
			if tr.IsError {
				b.WriteString("true] ")
			} else {
				b.WriteString("false] ")
			}
			b.WriteString(text + "\n")
		}
	}
	return b.String()
}
