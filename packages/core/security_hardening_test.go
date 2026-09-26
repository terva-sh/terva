package core

import (
	"sync"
	"testing"
)

// TestAgentToolsContextConcurrent exercises the /context data-race fix: the size
// view reads the tool registry and per-turn context provider via lock-guarded
// accessors while extension/MCP/trust/lore reloads swap them on other goroutines.
// Run under -race — an unlocked field read would trip the detector.
func TestAgentToolsContextConcurrent(t *testing.T) {
	a := newTestAgent(nil, "fake-model", "system", Registry{})

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
					_ = a.FramePreview()
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
				testFrameOf(a).setHost(func() string { return "ctx" })
				testFrameOf(a).setPeek(func() string { return "peek" })
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
}
