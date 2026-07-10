package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// LogError writes to a sidecar (<name>.errors.jsonl) ALONGSIDE the transcript,
// never into it: the transcript keeps its fixed record vocabulary for
// replay/resume/compaction. The sidecar is created lazily (a clean session
// leaves none) and stamps the session's provider/model on each row.
func TestLogErrorWritesSidecarNotTranscript(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-codex", "gpt-5.5", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}

	// A clean session opens no sidecar.
	if _, statErr := os.Stat(s.ErrorLogPath()); !os.IsNotExist(statErr) {
		t.Fatalf("sidecar should not exist before any error, stat err = %v", statErr)
	}
	if want := filepath.Join(filepath.Dir(path), "s.errors.jsonl"); s.ErrorLogPath() != want {
		t.Errorf("ErrorLogPath = %q, want %q", s.ErrorLogPath(), want)
	}

	// A normal message on the transcript, then two errors on the sidecar.
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.LogError("400 from provider: context canceled"); err != nil {
		t.Fatalf("LogError: %v", err)
	}
	if err := s.LogError("stream reset by peer"); err != nil {
		t.Fatalf("LogError: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The transcript must carry no error row — its vocabulary is unchanged.
	transcript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(transcript)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("transcript line not JSON: %q", line)
		}
		if row["type"] == "error" || row["error"] != nil {
			t.Errorf("error leaked into the transcript: %q", line)
		}
	}

	// The sidecar holds exactly the two errors, newest-schema, provider-stamped.
	sidecar, err := os.ReadFile(s.ErrorLogPath())
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(sidecar)), "\n")
	if len(lines) != 2 {
		t.Fatalf("sidecar rows = %d, want 2:\n%s", len(lines), sidecar)
	}
	var first sessionError
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("sidecar row not JSON: %v", err)
	}
	if first.Error != "400 from provider: context canceled" {
		t.Errorf("row error = %q", first.Error)
	}
	if first.Provider != "openai-codex" || first.Model != "gpt-5.5" {
		t.Errorf("row not stamped with provider/model: %+v", first)
	}
	if first.Time.IsZero() {
		t.Error("row missing timestamp")
	}
}

// A nil/empty error, a live-only session (no path), and a nil session are all
// no-ops — LogError never creates a stray file or panics.
func TestLogErrorNoOpCases(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.LogError("   "); err != nil {
		t.Errorf("blank error should be a no-op, got %v", err)
	}
	if _, statErr := os.Stat(s.ErrorLogPath()); !os.IsNotExist(statErr) {
		t.Error("blank error must not create the sidecar")
	}

	var nilSess *Session
	if err := nilSess.LogError("boom"); err != nil {
		t.Errorf("nil session LogError should be a no-op, got %v", err)
	}
	if nilSess.ErrorLogPath() != "" {
		t.Error("nil session ErrorLogPath should be empty")
	}
}

// Error sidecars share the .jsonl extension but are not transcripts: session
// directory scans must skip them. Listing one would surface a blank entry in
// /sessions //continue (and LatestSession could resume it); pruning one would
// silently destroy the failure record, because sidecar rows carry no "message"
// lines and so read as an empty session.
func TestErrorSidecarIsNotASession(t *testing.T) {
	root := testsupport.TempDir(t)
	const cwd = "/ws"
	s, err := NewSession(root, cwd, "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.LogError("provider exploded"); err != nil {
		t.Fatal(err)
	}
	sidecar := s.ErrorLogPath()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// An orphan sidecar (transcript already gone) must be invisible too.
	orphan := filepath.Join(SessionsDir(root, cwd), "gone.errors.jsonl")
	if err := os.WriteFile(orphan, []byte(`{"time":"2026-07-08T00:00:00Z","error":"x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := ListSessions(root, cwd); len(got) != 1 || got[0] != s.Path {
		t.Errorf("ListSessions = %v, want exactly the transcript %q", got, s.Path)
	}
	if got := LatestSession(root, cwd); got != s.Path {
		t.Errorf("LatestSession = %q, want %q", got, s.Path)
	}

	// Prune reads sidecars as "no messages" — it must not touch them.
	PruneEmptySessions(root, cwd)
	for _, p := range []string{s.Path, sidecar, orphan} {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("%s should survive PruneEmptySessions: %v", filepath.Base(p), statErr)
		}
	}
}

// LogError must scrub secret-shaped substrings and bound the length before the
// error reaches the durable sidecar: provider/auth failures routinely
// stringify an Authorization header, a tokened callback URL, or a whole
// response body, any of which can carry a live credential.
func TestLogErrorRedactsSecretsInSidecar(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "openai-codex", "gpt-5.5", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	raw := "401 Unauthorized: request failed. " +
		"Authorization: Bearer sk-ant-api03-DEADBEEFdeadbeef1234567890 " +
		"redirect https://auth.example.com/cb?state=ok&access_token=SUPERSECRETVALUE&foo=bar " +
		`body {"api_key":"AKIAIOSFODNN7EXAMPLE","note":"keep"}`
	if err := s.LogError(raw); err != nil {
		t.Fatalf("LogError: %v", err)
	}

	data, err := os.ReadFile(s.ErrorLogPath())
	if err != nil {
		t.Fatal(err)
	}
	var row sessionError
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatalf("sidecar row not JSON: %v", err)
	}
	got := row.Error

	for _, secret := range []string{
		"sk-ant-api03-DEADBEEFdeadbeef1234567890",
		"SUPERSECRETVALUE",
		"AKIAIOSFODNN7EXAMPLE",
	} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q leaked into sidecar: %q", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected a [REDACTED] marker, got %q", got)
	}
	// Non-secret diagnostic context must survive so the sidecar stays useful.
	if !strings.Contains(got, "401 Unauthorized") {
		t.Errorf("diagnostic context should survive redaction, got %q", got)
	}
	if !strings.Contains(got, "auth.example.com/cb") || !strings.Contains(got, "foo=bar") {
		t.Errorf("non-secret URL context should survive, got %q", got)
	}
}

// A giant provider response body can't grow the sidecar without limit: the
// stored error is length-bounded with a truncation marker.
func TestLogErrorBoundsLengthInSidecar(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.LogError(strings.Repeat("x", 100_000)); err != nil {
		t.Fatalf("LogError: %v", err)
	}
	data, err := os.ReadFile(s.ErrorLogPath())
	if err != nil {
		t.Fatal(err)
	}
	var row sessionError
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatal(err)
	}
	if len(row.Error) > maxSidecarErrorLen+64 {
		t.Errorf("stored error length = %d, want <= %d (+ marker)", len(row.Error), maxSidecarErrorLen)
	}
	if !strings.Contains(row.Error, "truncated") {
		t.Errorf("expected a truncation marker in the stored error")
	}
}

// An errored-but-empty session (no messages) discards its transcript on Close;
// the paired sidecar must not be orphaned.
func TestLogErrorSidecarRemovedWithEmptyTranscript(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogError("failed before any message landed"); err != nil {
		t.Fatal(err)
	}
	sidecar := s.ErrorLogPath()
	if _, statErr := os.Stat(sidecar); statErr != nil {
		t.Fatalf("sidecar should exist after LogError: %v", statErr)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("empty transcript should be removed on Close")
	}
	if _, statErr := os.Stat(sidecar); !os.IsNotExist(statErr) {
		t.Error("orphaned sidecar should be removed with its empty transcript")
	}
}
