package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/core"
)

// auditLog is an append-only security record of tool calls: every call the
// agent attempts, the approval mode in force, and the gate's allow/deny
// decision — so even a yolo session (which prompts for nothing) leaves a
// durable, reviewable record of what ran and why it was permitted. This is the
// piece the session transcript can't give you: the transcript records the
// command, but not the approval decision behind it.
//
// It lives at $TERVA_HOME/logs/audit.log, mode 0600. logs/ is excluded from the
// read jail precisely because tool args (bash commands, file writes) can carry
// secrets, so the audit log inherits that protection. One JSON object per line
// (JSONL) — greppable by `tool`, parseable with jq.
//
// EVERY tool call is recorded, reads included: an audit log with blind spots
// isn't one. Filter at read time (`jq 'select(.tool=="bash")'`). That covers
// all three doors into the gate, distinguished by `via`: the model tool-call
// ladder (BuildBeforeToolExecute), extension host_tool_call dispatch, and
// code_execution's script bindings — the latter two check the gate outside
// the ladder, so they record their own lines.
//
// auditSink is the process-wide instance, set once at startup (Run): a single
// append-only file per process, so a package var fits it the way a logger
// would. Nil until set — and every method is nil-safe — so tests and any
// pre-startup path are a clean no-op. Records carry the PID; per-session
// correlation is deferred (a shared mutable session field would race across
// concurrent sub-agents), and the timestamp already lines records up with the
// session transcript.
var auditSink *auditLog

type auditLog struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	failed bool
}

// newAuditLog returns an audit logger writing under home/logs. The file is
// opened lazily on the first record, so a session that runs no tools never
// touches disk. A nil *auditLog is a safe no-op.
func newAuditLog(home string) *auditLog {
	return &auditLog{path: filepath.Join(home, "logs", "audit.log")}
}

// The three doors into the permission gate, named on each record's `via` so a
// review can tell a model-issued call from an extension's host_tool_call from
// a script binding. Absent on lines written before the field existed, which
// were all auditViaToolCall — the other two doors recorded nothing then.
const (
	auditViaToolCall      = "tool_call"
	auditViaHostToolCall  = "host_tool_call"
	auditViaScriptBinding = "code_execution"
)

type auditRecord struct {
	Time     string          `json:"time"`
	PID      int             `json:"pid"`
	Via      string          `json:"via,omitempty"`
	Tool     string          `json:"tool"`
	Mode     string          `json:"mode"`
	Decision string          `json:"decision"` // "allow" | "deny"
	Reason   string          `json:"reason,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
}

// Record appends one decision line. now is injected for testability. Best
// effort: a write failure is latched so a broken log never breaks tool
// execution, and a nil receiver is a no-op.
func (a *auditLog) Record(now time.Time, via, tool string, args json.RawMessage, mode string, allowed bool, reason string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed {
		return
	}
	if a.f == nil {
		if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
			a.failed = true
			return
		}
		f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			a.failed = true
			return
		}
		a.f = f
	}
	decision := "allow"
	if !allowed {
		decision = "deny"
	}
	line, err := json.Marshal(auditRecord{
		Time:     now.UTC().Format(time.RFC3339),
		PID:      os.Getpid(),
		Via:      via,
		Tool:     tool,
		Mode:     mode,
		Decision: decision,
		Reason:   reason,
		Args:     capAuditArgs(args),
	})
	if err != nil {
		return
	}
	if _, err := a.f.Write(append(line, '\n')); err != nil {
		a.failed = true
	}
}

// Close closes the backing file (if any). Safe on a nil receiver.
func (a *auditLog) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.f != nil {
		_ = a.f.Close()
		a.f = nil
	}
	a.mu.Unlock()
}

// capAuditArgs bounds the logged args so a giant write payload can't bloat one
// audit line — the command/path that matters for a security review is at the
// front. Invalid JSON is dropped rather than corrupting the line.
func capAuditArgs(args json.RawMessage) json.RawMessage {
	const max = 2048
	if len(args) == 0 || !json.Valid(args) {
		return nil
	}
	if len(args) <= max {
		return args
	}
	note, _ := json.Marshal(fmt.Sprintf("%s…[truncated %d bytes]", string(args[:max]), len(args)-max))
	return note
}

// InitAudit installs the process-wide tool-audit sink. Hosts call this once at
// startup; the sink itself stays unexported so nothing outside can swap it
// mid-run.
func InitAudit(home string) { auditSink = newAuditLog(home) }

// recordGateAudit writes one audit line for a gate decision reached outside
// the model tool-call ladder (host_tool_call, code_execution bindings) — the
// ladder's deferred audit never sees those checks, so each door records its
// own. A nil gate reports the empty mode, matching the ladder's spelling for
// a gateless (pure yolo) session.
func recordGateAudit(via, tool string, args json.RawMessage, gate *core.ConfirmGate, allowed bool, reason string) {
	mode := ""
	if gate != nil {
		mode = string(gate.Mode())
	}
	auditSink.Record(time.Now(), via, tool, args, mode, allowed, reason)
}
