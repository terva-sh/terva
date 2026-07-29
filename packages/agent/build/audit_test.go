package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// readAuditLines parses every JSONL record from home/logs/audit.log.
func readAuditLines(t *testing.T, home string) []auditRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "logs", "audit.log"))
	if err != nil {
		t.Fatalf("read audit.log: %v", err)
	}
	var recs []auditRecord
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var r auditRecord
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Fatalf("bad audit line %q: %v", ln, err)
		}
		recs = append(recs, r)
	}
	return recs
}

// Record writes one allow and one deny line with the expected fields, and the
// file is created 0600 (it can hold secret-bearing args).
func TestAuditLogRecord(t *testing.T) {
	home := testsupport.TempDir(t)
	a := newAuditLog(home)
	now := time.Unix(1_700_000_000, 0)
	a.Record(now, auditViaToolCall, "bash", json.RawMessage(`{"command":"ls -la"}`), "yolo", true, "")
	a.Record(now, auditViaToolCall, "bash", json.RawMessage(`{"command":"rm -rf /"}`), "ask", false, "denied by rule")
	a.Close()

	recs := readAuditLines(t, home)
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0].Tool != "bash" || recs[0].Mode != "yolo" || recs[0].Decision != "allow" || recs[0].Via != auditViaToolCall {
		t.Errorf("allow record wrong: %+v", recs[0])
	}
	if !strings.Contains(string(recs[0].Args), "ls -la") {
		t.Errorf("allow record args missing command: %s", recs[0].Args)
	}
	if recs[1].Decision != "deny" || recs[1].Reason != "denied by rule" || recs[1].Mode != "ask" {
		t.Errorf("deny record wrong: %+v", recs[1])
	}
	if recs[0].PID == 0 || recs[0].Time == "" {
		t.Errorf("record missing pid/time: %+v", recs[0])
	}

	info, err := os.Stat(filepath.Join(home, "logs", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits (Go reports 0666 regardless), so the
	// 0600 we pass to OpenFile is only verifiable on POSIX.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("audit.log mode = %v, want 0600 (it can carry secrets)", info.Mode().Perm())
	}
}

// A nil sink and a no-tool run never touch disk; oversized args are truncated.
func TestAuditLogEdgeCases(t *testing.T) {
	// nil receiver is a no-op (the off switch).
	var nilLog *auditLog
	nilLog.Record(time.Unix(0, 0), auditViaToolCall, "bash", nil, "yolo", true, "") // must not panic

	home := testsupport.TempDir(t)
	a := newAuditLog(home)
	a.Close() // closing before any record must not create the file
	if _, err := os.Stat(filepath.Join(home, "logs", "audit.log")); !os.IsNotExist(err) {
		t.Error("audit.log created without any record")
	}

	// Oversized args collapse to a truncation note instead of a giant line.
	big := `{"command":"` + strings.Repeat("x", 5000) + `"}`
	a.Record(time.Unix(0, 0), auditViaToolCall, "bash", json.RawMessage(big), "yolo", true, "")
	a.Close()
	recs := readAuditLines(t, home)
	if len(recs) != 1 || !strings.Contains(string(recs[0].Args), "truncated") {
		t.Errorf("oversized args not truncated: %s", recs[0].Args)
	}
	if len(recs[0].Args) > 2200 {
		t.Errorf("truncated args still too long: %d bytes", len(recs[0].Args))
	}
}

// The real BeforeToolExecute ladder records through the process sink: an
// allowed bash call lands in the audit log with its command.
func TestBeforeToolExecuteAudits(t *testing.T) {
	home := testsupport.TempDir(t)
	prev := auditSink
	auditSink = newAuditLog(home)
	t.Cleanup(func() { auditSink.Close(); auditSink = prev })

	// gate=nil → no gate check, call is allowed (mode reports empty).
	fn := BuildBeforeToolExecute(nil, nil, nil)
	call := provider.ToolCallBlock{ID: "T1", Name: "bash", Arguments: []byte(`{"command":"echo hi"}`)}
	if allowed, _, _ := fn(t.Context(), call); !allowed {
		t.Fatal("expected the call to be allowed")
	}
	auditSink.Close()

	recs := readAuditLines(t, home)
	if len(recs) != 1 {
		t.Fatalf("want 1 audit record from the ladder, got %d", len(recs))
	}
	if recs[0].Tool != "bash" || recs[0].Decision != "allow" || !strings.Contains(string(recs[0].Args), "echo hi") {
		t.Errorf("ladder audit record wrong: %+v", recs[0])
	}
	if recs[0].Via != auditViaToolCall {
		t.Errorf("ladder door must stamp via=%s, got %q", auditViaToolCall, recs[0].Via)
	}
}
