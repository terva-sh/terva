package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/testsupport"
)

// The dump's whole promise is that it shows the prompt a live session would
// send, and durable memory is part of that prompt for any home that has one.
// The live flow injects it at session bind; the dump binds no session, so it
// must make the same call itself. This went unnoticed because every home the
// dump had been run against was empty: the eval harness pre-flighted a
// memory-policy overlay as "no difference" while the real arms differed by
// the whole policy block.
func TestPromptDumpIncludesMemoryBlock(t *testing.T) {
	home := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "memory", "user.md"),
		[]byte("- a seeded fact the dump must show\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERVA_HOME", home)

	args := build.Args{DumpPrompt: "sizes", Prompt: "verify",
		Provider: "anthropic", Model: "claude-haiku-4-5-20251001"}
	out, err := promptDumpText(args)
	if err != nil {
		t.Fatal(err)
	}
	// Scoped to the system-by-source table: the memory TOOL renders an
	// identical-looking "  memory" row in the tools table, and matching the
	// whole output would pass on that row with the block still missing.
	_, system, found := strings.Cut(out, "system — by source")
	if !found {
		t.Fatalf("sizes dump has no system-by-source table:\n%s", out)
	}
	if !strings.Contains(system, "\n  memory") {
		t.Fatalf("sizes dump against a home with memory has no memory system row:\n%s", out)
	}

	// And the block itself must be in the assembled text, not just a row in
	// the accounting.
	args.DumpPrompt = "text"
	out, err = promptDumpText(args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a seeded fact the dump must show") {
		t.Fatalf("text dump does not carry the seeded memory entry:\n%s", out)
	}
}
