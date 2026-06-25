package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// TestSwarmEmitterMirrorDormantUntilStdoutBreaks regresses the
// "everything is doubled after reopening a swarm agent" bug.
//
// Symptom: events.jsonl held two copies of every event because the
// child mirrored each event to disk AND the supervisor parsed the
// child's stdout and appended each event to disk too. On next terva
// launch the replay produced two transcript lines per real one.
//
// Fix invariant: while stdout writes succeed (i.e. the supervisor is
// alive on the other end of the pipe), the child's mirror writes
// NOTHING. Only when a stdout write fails (broken pipe → orphan)
// does the mirror take over so events still get persisted.
func TestSwarmEmitterMirrorDormantUntilStdoutBreaks(t *testing.T) {
	// Real *os.File for the emitter's stdout-equivalent so the
	// emitter's write() path (which expects *os.File) actually runs.
	stdoutPath := filepath.Join(testsupport.TempDir(t), "stdout.fifo")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	defer stdoutFile.Close()

	// Mirror writes go to a separate events.jsonl that we can read
	// at the end to assert how many events the mirror emitted.
	mirrorPath := filepath.Join(testsupport.TempDir(t), "events.jsonl")
	mirror, err := swarm.OpenEventLog(mirrorPath)
	if err != nil {
		t.Fatalf("open mirror: %v", err)
	}
	defer mirror.Close()

	em := newSwarmEmitter(stdoutFile, mirror)

	// Healthy stdout: emit three events. Mirror must stay empty.
	em.emit("turn_start", map[string]any{"step": 1})
	em.emit("assistant_message", map[string]any{"text": "hi"})
	em.emit("turn_end", map[string]any{"step": 1})

	got, err := swarm.ReadEventLog(mirrorPath)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("mirror wrote %d events while supervisor was alive; want 0 (every event would otherwise double on the next reload)\n%+v",
			len(got), got)
	}

	// Simulate supervisor death: close stdout so the next Write
	// returns EBADF / broken pipe. The emitter must flip into
	// orphan mode and start writing through the mirror.
	if err := stdoutFile.Close(); err != nil {
		t.Fatalf("close stdout: %v", err)
	}

	em.emit("assistant_message", map[string]any{"text": "after orphan"})
	em.emit("turn_end", map[string]any{"step": 2})

	got, err = swarm.ReadEventLog(mirrorPath)
	if err != nil {
		t.Fatalf("read mirror post-orphan: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("mirror failed to take over after stdout died: got %d events", len(got))
	}
	if got[len(got)-1].Type != "turn_end" {
		t.Errorf("last mirrored event type = %q; want turn_end", got[len(got)-1].Type)
	}
}

// TestSwarmEmitterStdoutShapeMatchesSupervisorParser pins the
// wire-format contract: each emitted event lands on stdout as one
// JSON object per line with type+time at top level alongside the
// data fields. The supervisor's runner parses this exact shape.
func TestSwarmEmitterStdoutShapeMatchesSupervisorParser(t *testing.T) {
	// Pipe so we can read what the emitter wrote.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()

	em := newSwarmEmitter(w, nil)
	em.emit("turn_start", map[string]any{"step": 1})
	_ = w.Close()

	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	// One trailing newline => one event line.
	lines := bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected 1 event line, got %d: %q", len(lines), body)
	}
	var flat map[string]any
	if err := json.Unmarshal(lines[0], &flat); err != nil {
		t.Fatalf("not valid json: %v\n%s", err, lines[0])
	}
	if flat["type"] != "turn_start" {
		t.Errorf("type field missing or wrong: %v", flat["type"])
	}
	if _, ok := flat["time"].(string); !ok {
		t.Errorf("time field missing: %v", flat["time"])
	}
	if flat["step"] != float64(1) {
		t.Errorf("step field missing or wrong: %v", flat["step"])
	}
}
