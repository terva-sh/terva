package swarm

import (
	"encoding/json"
	"os"
	"path/filepath"

	"terva.sh/terva/packages/agent/deliverable"
)

// captureDeliverable derives the agent's structured deliverable once a
// task-level turn ends, when the spawn carried a schema. Two routes, in
// order: the deliver_result side-channel file (native children — already
// validated child-side, but re-validated here because the file is just a
// file and the schema is the contract), else fenced-JSON extraction from
// the final assistant message (foreign workers, or a native child that
// ignored its tool). Invalid or missing documents record the error
// instead of a document — ABSENT with a reason, the RAATI ballot stance —
// and a later turn (e.g. after a corrective user nudge) can overwrite
// either outcome, so the capture is last-turn-wins like lastAssistant.
func (a *Agent) captureDeliverable() {
	if len(a.Schema) == 0 {
		return
	}
	var doc json.RawMessage
	var derr error
	if raw, err := os.ReadFile(filepath.Join(filepath.Dir(a.EventLogPath), deliverable.FileName)); err == nil && len(raw) > 0 {
		doc = json.RawMessage(raw)
	} else {
		a.mu.Lock()
		text := a.lastAssistant
		if len(a.preGuardAssistant) > len(text) {
			text = a.preGuardAssistant
		}
		a.mu.Unlock()
		doc, derr = deliverable.Extract(text)
	}
	if derr == nil {
		derr = deliverable.Validate(a.Schema, doc)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if derr != nil {
		a.deliverable = nil
		a.deliverableErr = derr.Error()
		return
	}
	a.deliverable = doc
	a.deliverableErr = ""
}
