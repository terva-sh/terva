package transcripttest

import (
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Recorder is a store that keeps every write it receives, for a test that
// asks what an agent wrote rather than whether a store can rebuild it. Attach
// it with Agent.AttachTranscriptStore.
//
// It implements TranscriptStore only, not TranscriptDiagnostics, so it never
// receives the diagnostic records. A test that needs those wants a store that
// keeps them, not one that pretends to.
//
// It is safe to read while the agent writes.
type Recorder struct {
	mu          sync.Mutex
	messages    []provider.Message
	compactions []core.CompactResult
	usage       []core.UsageRecord
	groups      []string
	images      []string
}

var _ core.TranscriptStore = (*Recorder)(nil)

func (r *Recorder) AppendMessage(m provider.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, m)
	return nil
}

func (r *Recorder) AppendCompaction(_ []provider.Message, res core.CompactResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.compactions = append(r.compactions, res)
	return nil
}

func (r *Recorder) AppendUsage(u core.UsageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage = append(r.usage, u)
	return nil
}

func (r *Recorder) AppendToolGroupActivation(group string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groups = append(r.groups, group)
	return nil
}

func (r *Recorder) AppendImageExclusion(sha256Hex string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images = append(r.images, sha256Hex)
	return nil
}

// Usage returns the usage records of the given kind, in the order written.
func (r *Recorder) Usage(kind core.UsageKind) []core.UsageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []core.UsageRecord
	for _, u := range r.usage {
		if u.Kind == kind {
			out = append(out, u)
		}
	}
	return out
}

// Compactions returns the result of each compaction written, in order.
func (r *Recorder) Compactions() []core.CompactResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.CompactResult(nil), r.compactions...)
}

// ToolGroups returns each tool-group activation written, in order.
func (r *Recorder) ToolGroups() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.groups...)
}
