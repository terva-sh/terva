package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// staticCtxSource is a tool source that also contributes static context
// (the optional StaticContext() that MergeExtensionTools folds in).
type staticCtxSource struct {
	fakeToolSource
	static string
}

func (s *staticCtxSource) StaticContext() string { return s.static }

// MergeExtensionTools folds an extension's static context contribution
// into the cached system prompt, idempotently, and reflects updates.
func TestMergeFoldsStaticContextIntoSystemPrompt(t *testing.T) {
	r := &Resolved{ToolRegistry: core.Registry{}, CWD: t.TempDir()}

	const marker = "TASKS-POLICY-keep-one-active"
	r.MergeExtensionTools(&staticCtxSource{static: marker})
	if !strings.Contains(r.SystemPrompt, marker) {
		t.Fatalf("static context not folded into system prompt:\n%s", r.SystemPrompt)
	}

	// Re-merging the same static context is a no-op (won't bust the cache).
	before := r.SystemPrompt
	r.MergeExtensionTools(&staticCtxSource{static: marker})
	if r.SystemPrompt != before {
		t.Error("re-merge with unchanged static context changed the prompt")
	}

	// Changing it replaces the old block.
	r.MergeExtensionTools(&staticCtxSource{static: "NEW-POLICY-text"})
	if !strings.Contains(r.SystemPrompt, "NEW-POLICY-text") {
		t.Errorf("updated static context not reflected:\n%s", r.SystemPrompt)
	}
	if strings.Contains(r.SystemPrompt, marker) {
		t.Errorf("stale static context still present:\n%s", r.SystemPrompt)
	}
}

// A source with no static context (e.g. an MCP source) leaves the
// prompt's addendum untouched.
func TestMergeWithoutStaticContextIsClean(t *testing.T) {
	r := &Resolved{ToolRegistry: core.Registry{}, CWD: t.TempDir()}
	r.MergeExtensionTools(&fakeToolSource{infos: []ExtensionToolInfo{
		{Extension: "x", Name: "plain_tool", Schema: []byte(`{}`)},
	}})
	if strings.Contains(r.SystemPrompt, "extension-context") {
		t.Errorf("unexpected context block with no static contribution:\n%s", r.SystemPrompt)
	}
}
