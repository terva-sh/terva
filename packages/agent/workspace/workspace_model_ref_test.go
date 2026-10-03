package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// sharedIDWorkspace is a workspace on cpa, over a catalog where anthropic and
// cpa both list one model id with anthropic first. A bare id that skipped the
// workspace's provider resolved to anthropic. diags collects what the
// workspace reported.
func sharedIDWorkspace(t *testing.T) (*Workspace, *[]string) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	modelreg.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "ref-shared"},
		{Provider: "cpa", ID: "ref-shared"},
		{Provider: "cpa", ID: "ref-session"},
		{Provider: "openrouter", ID: "cpa/ref-shared"},
	})
	t.Cleanup(func() { modelreg.SetUserModels(nil) })
	var diags []string
	return &Workspace{
		ctx:      context.Background(),
		diag:     func(s string) { diags = append(diags, s) },
		provider: "cpa",
		model:    "ref-session",
	}, &diags
}

func TestTheTitleModelResolvesByProvider(t *testing.T) {
	w, diags := sharedIDWorkspace(t)
	for _, tc := range []struct{ ref, wantProv, wantModel string }{
		{"", "cpa", "ref-session"},
		{"ref-shared", "cpa", "ref-shared"},
		{"anthropic/ref-shared", "anthropic", "ref-shared"},
		{"cpa/ref-shared", "cpa", "ref-shared"},
		{"no-such-model", "cpa", "ref-session"},
	} {
		prov, model := w.titleModel(tc.ref, "cpa", "ref-session")
		if prov != tc.wantProv || model != tc.wantModel {
			t.Errorf("auto_title_model %q runs on %s/%s, want %s/%s", tc.ref, prov, model, tc.wantProv, tc.wantModel)
		}
	}
	// The setting is read on every title pass. A second pass over the same
	// value reports nothing new.
	w.titleModel("cpa/ref-shared", "cpa", "ref-session")

	// cpa/ref-shared is also an OpenRouter id, so the user is told which
	// reading won and how to write the other, once.
	if len(*diags) != 1 || !strings.Contains((*diags)[0], "openrouter/cpa/ref-shared") {
		t.Errorf("diagnostics = %q, want one naming openrouter/cpa/ref-shared", *diags)
	}
}

func TestATalkootMemberModelResolvesByProvider(t *testing.T) {
	w, diags := sharedIDWorkspace(t)
	for _, tc := range []struct{ ref, wantProv, wantModel string }{
		{"ref-shared", "cpa", "ref-shared"},
		{"anthropic/ref-shared", "anthropic", "ref-shared"},
	} {
		prov, model, _, err := w.memberModel(talkoot.Member{ID: "m", Model: tc.ref})
		if err != nil {
			t.Fatalf("memberModel(%q): %v", tc.ref, err)
		}
		if prov != tc.wantProv || model != tc.wantModel {
			t.Errorf("member model %q runs on %s/%s, want %s/%s", tc.ref, prov, model, tc.wantProv, tc.wantModel)
		}
	}
	// cpa/ref-shared is also an OpenRouter id. A member is built more than
	// once in a run, and the note says so the first time only.
	for i := 0; i < 2; i++ {
		if _, _, _, err := w.memberModel(talkoot.Member{ID: "m", Model: "cpa/ref-shared"}); err != nil {
			t.Fatalf("memberModel(cpa/ref-shared): %v", err)
		}
	}
	if len(*diags) != 1 || !strings.Contains((*diags)[0], "openrouter/cpa/ref-shared") {
		t.Errorf("diagnostics = %q, want one naming openrouter/cpa/ref-shared", *diags)
	}
	if _, _, _, err := w.memberModel(talkoot.Member{ID: "m", Model: "no-such-model"}); err == nil {
		t.Error("an unknown member model resolved")
	}
}
