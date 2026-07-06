package core

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestSessionConcurrentWrites exercises the write-serialization fix: a session
// can have concurrent writers (a web client's clear/compact writing a checkpoint
// while a turn on another connection persists messages), and the bufio.Writer is
// not goroutine-safe. Without writeMu these interleave and corrupt the JSONL.
// Run under -race; also asserts every line is valid JSON (no torn writes).
func TestSessionConcurrentWrites(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "prov", "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello there"}}}

	var wg sync.WaitGroup
	for _, writer := range []func(){
		func() { // a turn persisting messages
			for i := 0; i < 60; i++ {
				_ = s.AppendMessage(msg)
			}
		},
		func() { // the clear/compact checkpoint path
			for i := 0; i < 60; i++ {
				_ = s.AppendCompaction(nil)
			}
		},
		func() { // usage rows
			for i := 0; i < 60; i++ {
				_ = s.AppendUsage(provider.Usage{}, provider.Usage{})
			}
		},
	} {
		wg.Add(1)
		go func(w func()) { defer wg.Done(); w() }(writer)
	}
	wg.Wait()

	// Read BEFORE Close: writeLine flushes on every write, so the file is current
	// now, and Close would prune this fresh session if the last counter update was
	// AppendCompaction(nil) (messagesAppended → 0) — an orthogonal, correct
	// behavior we don't want racing the assertion.
	b, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	for i, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("line %d is not valid JSON (interleaved concurrent write?): %q: %v", i, line, err)
		}
	}
}

// TestAgentToolsContextConcurrent exercises the /context data-race fix: the size
// view reads the tool registry and per-turn context provider via lock-guarded
// accessors while extension/MCP/trust/lore reloads swap them on other goroutines.
// Run under -race — an unlocked field read would trip the detector.
func TestAgentToolsContextConcurrent(t *testing.T) {
	a := NewAgent(nil, "fake-model", "system", Registry{})

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = a.ToolsSnapshot().Specs()
					_ = a.ContextPreview()
				}
			}
		}()
	}

	var writers sync.WaitGroup
	for i := 0; i < 3; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for j := 0; j < 300; j++ {
				a.SetTools(Registry{})
				a.SetContextProvider(func() string { return "ctx" })
				a.SetContextProviderPeek(func() string { return "peek" })
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
}
