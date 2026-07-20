package workspace

import (
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
)

// TestOverrideClientUnknownModel: a per-generation override (Phase 7) naming a
// model that isn't in the catalog is a clean, up-front CodeBadRequest — not a
// mid-stream failure once generation has started.
func TestOverrideClientUnknownModel(t *testing.T) {
	w := &Workspace{}
	base := build.Args{Provider: "openai", Model: "gpt-5"}
	_, _, err := w.overrideClient(base, "openai", "definitely-not-a-real-model")
	if err == nil {
		t.Fatal("expected an error for an unknown override model")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error = %q, want it to name the unknown model", err)
	}
	var werr *ctrlproto.Error
	if !errors.As(err, &werr) || werr.Code != ctrlproto.CodeBadRequest {
		t.Errorf("want a CodeBadRequest ctrlproto.Error, got %v", err)
	}
}

// TestApplyCastModels: a per-actor pin (Phase 7) routes that member and leaves the
// rest — and its other fields — untouched; a pin for a non-member is ignored.
func TestApplyCastModels(t *testing.T) {
	cast := map[string]tools.CastMember{
		"Elira": {Persona: "clothier"},
		"Guard": {Card: "/x/guard.png"},
	}
	applyCastModels(cast, map[string]core.CastRoute{
		"Elira": {Provider: "openai", Model: "gpt-5"},
		"Ghost": {Provider: "x", Model: "y"}, // not in the cast — ignored
	})
	if got := cast["Elira"]; got.Provider != "openai" || got.Model != "gpt-5" {
		t.Errorf("pinned member not routed: %+v", got)
	}
	if cast["Elira"].Persona != "clothier" {
		t.Error("overlay clobbered a non-route field")
	}
	if cast["Guard"].Model != "" || cast["Guard"].Provider != "" {
		t.Errorf("unpinned member should stay unrouted: %+v", cast["Guard"])
	}
	if _, ok := cast["Ghost"]; ok {
		t.Error("a pin for a non-member must not add a member")
	}
}
