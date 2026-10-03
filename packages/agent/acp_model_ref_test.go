//go:build terva_acp

package agent

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The production switch, not the acp package's fake, resolves an editor's
// value. Two logged-in providers list one id, with anthropic first. A
// qualified value names its row, and a bare value stays on the provider the
// session is on.
func TestACPSwitchModelResolvesTheEditorsValueByProvider(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	modelreg.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "acp-ref-shared", ContextWindow: 100_000},
		{Provider: "openai", ID: "acp-ref-shared", ContextWindow: 100_000},
	})
	t.Cleanup(func() { modelreg.SetUserModels(nil) })

	ctx := context.Background()
	f := &acpFactory{ctx: ctx, args: build.Args{
		Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true,
	}, version: "test"}

	for _, tc := range []struct{ value, wantProv string }{
		{"acp-ref-shared", "openai"},
		{"openai/acp-ref-shared", "openai"},
		{"anthropic/acp-ref-shared", "anthropic"},
	} {
		sw, err := f.SwitchModel("openai", "gpt-5", tc.value)
		if err != nil {
			t.Fatalf("SwitchModel(%q): %v", tc.value, err)
		}
		if sw.Provider != tc.wantProv || sw.Model != "acp-ref-shared" {
			t.Errorf("SwitchModel(%q) = %s/%s, want %s/acp-ref-shared", tc.value, sw.Provider, sw.Model, tc.wantProv)
		}
	}
}

// A stored gateway id that now reads as provider/id lands on a provider the
// user may not be logged in to. The refusal then says how to write the
// gateway's row, which is the one place the resolver's warning reaches the
// user in ACP.
func TestACPSwitchModelRefusalNamesTheOtherReading(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	modelreg.SetUserModels([]provider.Model{
		{Provider: "openai", ID: "acp-ref-direct", ContextWindow: 100_000},
		{Provider: "anthropic", ID: "openai/acp-ref-direct", ContextWindow: 100_000},
	})
	t.Cleanup(func() { modelreg.SetUserModels(nil) })

	f := &acpFactory{ctx: context.Background(), args: build.Args{
		Provider: "anthropic", Model: "openai/acp-ref-direct", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true,
	}, version: "test"}
	if acpLoggedInProviders()["openai"] {
		t.Skip("an openai credential is configured on this machine outside the environment; the refusal cannot be reached")
	}
	_, err := f.SwitchModel("anthropic", "openai/acp-ref-direct", "openai/acp-ref-direct")
	if err == nil {
		t.Fatal("the switch onto a provider with no credential was not refused")
	}
	if !strings.Contains(err.Error(), "anthropic/openai/acp-ref-direct") {
		t.Errorf("the refusal %q does not say how to write the gateway's row", err)
	}
}
