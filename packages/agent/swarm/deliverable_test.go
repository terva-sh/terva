package swarm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/deliverable"
	"terva.sh/terva/packages/testsupport"
)

const capSchema = `{"type":"object","required":["verdict"],"properties":{"verdict":{"enum":["pass","fail"]}}}`

func capAgent(t *testing.T) (*Agent, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	return &Agent{
		ID:           "cap-test",
		Schema:       json.RawMessage(capSchema),
		EventLogPath: filepath.Join(dir, "events.jsonl"),
	}, dir
}

func TestCaptureDeliverableFromFile(t *testing.T) {
	a, dir := capAgent(t)
	if err := os.WriteFile(filepath.Join(dir, deliverable.FileName), []byte(`{"verdict":"pass"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.captureDeliverable()
	snap := a.Snapshot()
	if string(snap.Deliverable) != `{"verdict":"pass"}` || snap.DeliverableError != "" {
		t.Fatalf("snapshot = %s / %q", snap.Deliverable, snap.DeliverableError)
	}
}

func TestCaptureDeliverableRevalidatesFile(t *testing.T) {
	// The file is just a file; a corrupted or contract-violating one must
	// record the error, not pass through as validated.
	a, dir := capAgent(t)
	if err := os.WriteFile(filepath.Join(dir, deliverable.FileName), []byte(`{"verdict":"maybe"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.captureDeliverable()
	snap := a.Snapshot()
	if snap.Deliverable != nil || snap.DeliverableError == "" {
		t.Fatalf("bad file must record an error, got %s / %q", snap.Deliverable, snap.DeliverableError)
	}
}

func TestCaptureDeliverableFenceFallback(t *testing.T) {
	// No file (a foreign worker): the fence in the final message is the
	// document.
	a, _ := capAgent(t)
	a.mu.Lock()
	a.lastAssistant = "Review complete.\n```json\n{\"verdict\":\"fail\"}\n```\n"
	a.mu.Unlock()
	a.captureDeliverable()
	snap := a.Snapshot()
	if string(snap.Deliverable) != `{"verdict":"fail"}` || snap.DeliverableError != "" {
		t.Fatalf("snapshot = %s / %q", snap.Deliverable, snap.DeliverableError)
	}
}

func TestCaptureDeliverableAbsentWithReason(t *testing.T) {
	a, _ := capAgent(t)
	a.mu.Lock()
	a.lastAssistant = "All done, everything looks fine."
	a.mu.Unlock()
	a.captureDeliverable()
	snap := a.Snapshot()
	if snap.Deliverable != nil || snap.DeliverableError == "" {
		t.Fatalf("missing document must record a reason, got %s / %q", snap.Deliverable, snap.DeliverableError)
	}
}

func TestCaptureDeliverableNoSchemaNoop(t *testing.T) {
	a, dir := capAgent(t)
	a.Schema = nil
	if err := os.WriteFile(filepath.Join(dir, deliverable.FileName), []byte(`{"verdict":"pass"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.captureDeliverable()
	snap := a.Snapshot()
	if snap.Deliverable != nil || snap.DeliverableError != "" {
		t.Fatalf("schema-less spawn must not capture, got %s / %q", snap.Deliverable, snap.DeliverableError)
	}
}

func TestCaptureDeliverableLaterTurnHeals(t *testing.T) {
	// A failed capture (bad first turn) is overwritten by a later good one
	// — e.g. after the dispatcher nudges the child to re-emit.
	a, _ := capAgent(t)
	a.mu.Lock()
	a.lastAssistant = "no fence here"
	a.mu.Unlock()
	a.captureDeliverable()
	if snap := a.Snapshot(); snap.DeliverableError == "" {
		t.Fatal("first capture should have failed")
	}
	a.mu.Lock()
	a.lastAssistant = "corrected:\n```json\n{\"verdict\":\"pass\"}\n```"
	a.mu.Unlock()
	a.captureDeliverable()
	snap := a.Snapshot()
	if string(snap.Deliverable) != `{"verdict":"pass"}` || snap.DeliverableError != "" {
		t.Fatalf("heal failed: %s / %q", snap.Deliverable, snap.DeliverableError)
	}
}
