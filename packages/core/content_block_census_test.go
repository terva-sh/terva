package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// Every provider.Content kind must be handled by BOTH encoders, and this guard
// enrolls them itself rather than listing them.
//
// The failure it exists for is silent. Both encoders are switches with no
// default, so a content block nobody added a case for is DROPPED — at write
// time in the session file, at the connection boundary over ctrlproto — with no
// error anywhere. For CompactionBlock that would mean a resumed conversation
// quietly missing the backend's only encoding of everything the compaction
// replaced; terva cannot rebuild that blob, so the loss is permanent.
//
// Written to scan for its own subjects (the isContent marker) so a block type
// added next year is covered by having been written, not by someone remembering
// this file. The first run IS the audit.
func TestEveryContentBlockIsHandledByBothEncoders(t *testing.T) {
	blocks := contentBlockNames(t)
	if len(blocks) < 5 {
		t.Fatalf("scan found only %v — the isContent marker probably moved, "+
			"and a census that finds nothing passes vacuously", blocks)
	}

	for _, enc := range []struct{ what, path string }{
		{"session file (encodeWireBlocks)", "session.go"},
		{"ctrlproto (contentToWire)", "wire.go"},
	} {
		src := readFile(t, enc.path)
		for _, name := range blocks {
			if !strings.Contains(src, "case provider."+name+":") {
				t.Errorf("%s has no case for provider.%s — that block is silently dropped there", enc.what, name)
			}
		}
	}
}

// contentBlockNames returns every type in packages/provider that implements
// Content, found by its isContent marker rather than by a list kept here.
func contentBlockNames(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "provider")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`func \((\w+)\) isContent\(\) \{\}`)
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			names = append(names, m[1])
		}
	}
	return names
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The census proves a case exists; this proves the case is CORRECT. A block
// that encodes and decodes to something subtly different is worse than one that
// is dropped, because nothing downstream can tell.
func TestCompactionBlockRoundTripsBothEncoders(t *testing.T) {
	// Provenance is part of the block's identity: a round trip that dropped it
	// would make the blob look foreign to the provider that issued it, and the
	// recovery path would re-compact a conversation that never needed it.
	want := provider.CompactionBlock{ID: "cmp_abc123", Encrypted: "gAAAAABopaque==", Provider: "openai-codex"}
	msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{want}}

	t.Run("session file", func(t *testing.T) {
		wire := encodeWireMessage(msg)
		if len(wire.Content) != 1 {
			t.Fatalf("want 1 encoded block, got %d", len(wire.Content))
		}
		if wire.Content[0].Type != blockCompaction {
			t.Errorf("discriminator = %q, want %q", wire.Content[0].Type, blockCompaction)
		}
		got, ok := decodeWireBlock(wire.Content[0])
		if !ok {
			t.Fatal("decode rejected a block this package just encoded")
		}
		if got != provider.Content(want) {
			t.Errorf("round trip changed the block: %+v -> %+v", want, got)
		}
	})

	t.Run("ctrlproto", func(t *testing.T) {
		wire := contentToWire(msg.Content, true)
		if len(wire) != 1 {
			t.Fatalf("want 1 wire block, got %d", len(wire))
		}
		if wire[0].Type != "compaction_summary" {
			t.Errorf("discriminator = %q, want compaction_summary", wire[0].Type)
		}
		got := ContentFromWire(wire)
		if len(got) != 1 {
			t.Fatalf("want 1 block back, got %d", len(got))
		}
		if got[0] != provider.Content(want) {
			t.Errorf("round trip changed the block: %+v -> %+v", want, got[0])
		}
	})
}
