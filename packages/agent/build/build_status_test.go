package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/buildinfo"
	"terva.sh/terva/packages/testsupport"
)

func TestResolveRegistersAndBindsStatusTool(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	st, ok := r.ToolRegistry["terva_status"].(*tools.StatusTool)
	if !ok {
		t.Fatal("terva_status not registered in the tool registry")
	}
	if st.Provider != "openai" {
		t.Errorf("status provider = %q, want openai", st.Provider)
	}
	if st.Agent != nil {
		t.Error("status tool should have no agent bound before NewAgent")
	}

	ag := r.NewAgent()
	if st.Agent != ag {
		t.Error("NewAgent did not bind the live agent into terva_status")
	}

	// The system prompt should mention the tool when it's registered.
	if !strings.Contains(r.SystemPrompt, "terva_status") {
		t.Error("system prompt is missing the terva_status hint when the tool is registered")
	}
}

// TestResolveStampsStatusToolWithBuildInfo closes the full runtime wire:
// main records the build (buildinfo.Set) before Resolve, and Resolve →
// BuildToolRegistry stamps it into terva_status via buildinfo.Get(). The
// unit tests set StatusTool.Build directly; this proves the construction
// path actually reads the process-global.
func TestResolveStampsStatusToolWithBuildInfo(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	prev := buildinfo.Get() // process-global; restore so later tests see the original
	t.Cleanup(func() { buildinfo.Set(prev) })
	want := buildinfo.Info{Version: "9.9.9-test", Commit: "abc1234def", Date: "2026-07-12T00:00:00Z"}
	buildinfo.Set(want)

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	st, ok := r.ToolRegistry["terva_status"].(*tools.StatusTool)
	if !ok {
		t.Fatal("terva_status not registered")
	}
	if st.Build != want {
		t.Errorf("status tool Build = %+v, want %+v", st.Build, want)
	}
}

func TestResolveStatusToolRespectsToolGating(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	// --no-tools strips everything, including terva_status.
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", NoTools: true}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := r.ToolRegistry["terva_status"]; ok {
		t.Error("terva_status present despite --no-tools")
	}
	if strings.Contains(r.SystemPrompt, "terva_status") {
		t.Error("system prompt mentions terva_status despite --no-tools dropping the tool")
	}

	// An allowlist that omits terva_status excludes it.
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5", Tools: []string{"read"}}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := r.ToolRegistry["terva_status"]; ok {
		t.Error("terva_status present despite an allowlist of {read}")
	}

	// An allowlist that names terva_status includes it.
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5", Tools: []string{"terva_status"}}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := r.ToolRegistry["terva_status"]; !ok {
		t.Error("terva_status absent despite being named in the allowlist")
	}
}
