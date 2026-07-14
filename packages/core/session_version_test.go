package core

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// seed appends one real message. Close deletes a freshly-created session that
// never held any, so a session that is going to be RESUMED has to have said
// something first — which is also the only kind whose provenance anyone cares
// about.
func seed(t *testing.T, s *Session) {
	t.Helper()
	m := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}
	if err := s.AppendMessage(m); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
}

// metaRows reads back every meta row in a session file, in order. The rows are
// append-only and the loader keeps the last one, so read as a sequence they are
// the session's provenance timeline: which build, on which model, wrote the
// lines that follow.
func metaRows(t *testing.T, path string) []SessionMeta {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var out []SessionMeta
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		var row struct {
			Type string      `json:"type"`
			Meta SessionMeta `json:"meta"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil || row.Type != "meta" {
			continue
		}
		out = append(out, row.Meta)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

// TestResumeUnderANewBuildStampsIt is the whole point: a session outlives the
// build that created it, and the file has to be able to say so.
//
// The version was stamped once at NewSession and then re-emitted verbatim by
// every later meta row. So a session created on 0.122.0 and resumed for weeks
// afterwards still claimed 0.122.0 on rows written by builds that did not exist
// yet — and because the version rode along on the model-switch rows, the file
// did not merely omit the truth, it asserted the opposite.
func TestResumeUnderANewBuildStampsIt(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai-codex", "gpt-5.6-sol", "0.122.0")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	seed(t, s)
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := reopened.StampVersion("0.123.0"); err != nil {
		t.Fatalf("StampVersion: %v", err)
	}
	// The model switch AFTER the upgrade must carry the NEW build, not the one
	// that created the session. This is the assertion the old code failed.
	if err := reopened.UpdateModel("anthropic", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	rows := metaRows(t, path)
	if len(rows) != 3 {
		t.Fatalf("got %d meta rows, want 3 (create, upgrade, model switch): %+v", len(rows), rows)
	}
	if got := rows[0].Version; got != "0.122.0" {
		t.Errorf("the creating row says version %q, want 0.122.0 — history must not be rewritten", got)
	}
	if got := rows[1].Version; got != "0.123.0" {
		t.Errorf("the resume row says version %q, want 0.123.0", got)
	}
	if got, want := rows[2].Version, "0.123.0"; got != want {
		t.Errorf("the model-switch row written AFTER the upgrade says version %q, want %q — "+
			"a row must name the build that wrote it", got, want)
	}
	if got := rows[2].Model; got != "claude-sonnet-4-5" {
		t.Errorf("the model-switch row says model %q, want claude-sonnet-4-5", got)
	}
}

// TestResumeUnderTheSameBuildIsSilent: the common case is resuming under the
// build you left. That must not append a row saying nothing changed.
func TestResumeUnderTheSameBuildIsSilent(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai-codex", "gpt-5.6-sol", "0.123.0")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	seed(t, s)
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	for range 3 {
		reopened, _, err := OpenSession(path)
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		if err := reopened.StampVersion("0.123.0"); err != nil {
			t.Fatalf("StampVersion: %v", err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	if rows := metaRows(t, path); len(rows) != 1 {
		t.Errorf("got %d meta rows after three same-build resumes, want 1", len(rows))
	}
}

// TestUpdateModelToTheSameModelWritesNothing pins the noise fix. Startup applies
// the resolved model unconditionally, so a real session file opened three times
// began with three byte-identical meta rows.
func TestUpdateModelToTheSameModelWritesNothing(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai-codex", "gpt-5.6-sol", "0.123.0")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	for range 3 {
		if err := s.UpdateModel("openai-codex", "gpt-5.6-sol"); err != nil {
			t.Fatalf("UpdateModel: %v", err)
		}
	}
	if rows := metaRows(t, s.Path); len(rows) != 1 {
		t.Errorf("got %d meta rows, want 1 — re-applying the same model must write nothing", len(rows))
	}

	// A real switch still lands.
	if err := s.UpdateModel("anthropic", "claude-opus-4-8"); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	rows := metaRows(t, s.Path)
	if len(rows) != 2 {
		t.Fatalf("got %d meta rows after a real switch, want 2", len(rows))
	}
	if rows[1].Model != "claude-opus-4-8" || rows[1].Provider != "anthropic" {
		t.Errorf("switch row = %s/%s, want anthropic/claude-opus-4-8", rows[1].Provider, rows[1].Model)
	}
}
