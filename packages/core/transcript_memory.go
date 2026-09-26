package core

import (
	"strings"
	"sync"

	"terva.sh/terva/packages/provider"
)

// MemoryTranscriptStore is a TranscriptStore that keeps everything in memory.
// It is for hosts that persist nothing, tests, and a host that serializes the
// transcript itself when it chooses to. Transcript returns what a resume from
// it needs, reconstructed the way the JSONL loader reconstructs a file, so an
// agent resumed from either one starts in the same state.
//
// It keeps no diagnostic records. The zero value is ready to use, and it is
// safe for concurrent use.
type MemoryTranscriptStore struct {
	mu        sync.Mutex
	messages  []provider.Message
	usage     usageFold
	groups    []string
	groupSeen map[string]bool
	excluded  map[string]bool
}

var _ TranscriptStore = (*MemoryTranscriptStore)(nil)

// NewMemoryTranscriptStore returns an empty store.
func NewMemoryTranscriptStore() *MemoryTranscriptStore { return &MemoryTranscriptStore{} }

func (s *MemoryTranscriptStore) AppendMessage(m provider.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, copyMessage(m))
	return nil
}

// AppendCompaction replaces the kept messages with the compacted ones, as a
// compaction row does for the JSONL loader.
func (s *MemoryTranscriptStore) AppendCompaction(messages []provider.Message, res CompactResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = s.messages[:0:0]
	for _, m := range messages {
		s.messages = append(s.messages, copyMessage(m))
	}
	s.usage.compactedTo(messages)
	s.usage.compactionSpend(res.Usage)
	return nil
}

func (s *MemoryTranscriptStore) AppendUsage(r UsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage.usage(r.Usage, r.Cumulative, r.Kind != UsageTurn)
	return nil
}

func (s *MemoryTranscriptStore) AppendToolGroupActivation(group string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groupSeen == nil {
		s.groupSeen = map[string]bool{}
	}
	if !s.groupSeen[group] {
		s.groupSeen[group] = true
		s.groups = append(s.groups, group)
	}
	return nil
}

func (s *MemoryTranscriptStore) AppendImageExclusion(sha256Hex string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.excluded == nil {
		s.excluded = map[string]bool{}
	}
	s.excluded[strings.ToLower(sha256Hex)] = true
	return nil
}

// Transcript returns the conversation as a resume would load it: excluded
// images replaced by the rejected-image note wherever they appear, and any
// tool call left without a result given a stub, as the JSONL loader does.
// The result shares nothing with the store.
func (s *MemoryTranscriptStore) Transcript() Transcript {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := make([]provider.Message, 0, len(s.messages))
	for _, m := range s.messages {
		msgs = append(msgs, copyMessage(m))
	}
	if len(s.excluded) > 0 {
		msgs = applyImageExclusions(msgs, s.excluded)
	}
	cum, _, resume := s.usage.result()
	return Transcript{
		Messages:         repairToolUseResultPairs(msgs),
		Cumulative:       cum,
		ResumeContext:    resume,
		ActiveToolGroups: append([]string(nil), s.groups...),
	}
}

// copyMessage copies m deeply enough that nothing the store keeps is shared
// with a caller: the content slices, a tool result's inner content, the
// Meta map, and the bytes behind an image or a call's arguments. Without it,
// a caller that edits a message after appending it, or edits a Transcript it
// was handed, would change what a later resume loads.
func copyMessage(m provider.Message) provider.Message {
	m.Content = copyContent(m.Content)
	if m.Meta != nil {
		meta := make(map[string]string, len(m.Meta))
		for k, v := range m.Meta {
			meta[k] = v
		}
		m.Meta = meta
	}
	return m
}

func copyContent(in []provider.Content) []provider.Content {
	if in == nil {
		return nil
	}
	out := make([]provider.Content, len(in))
	for i, c := range in {
		switch b := c.(type) {
		case provider.ToolResultBlock:
			b.Content = copyContent(b.Content)
			c = b
		case provider.ImageBlock:
			b.Data = append([]byte(nil), b.Data...)
			c = b
		case provider.ToolCallBlock:
			b.Arguments = append([]byte(nil), b.Arguments...)
			c = b
		}
		out[i] = c
	}
	return out
}
