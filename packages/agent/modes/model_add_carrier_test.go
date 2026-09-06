package modes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/provider"
)

// addCarrier is a carrier that also serves the models.json surface. The shared
// fakeCarrier deliberately does not: modelParamsController type-asserts for it,
// and a fake that answered every optional controller would hide the case where
// a carrier serves none.
type addCarrier struct {
	*fakeCarrier
	added  []ctrlproto.ModelAddParams
	refuse error
}

func (c *addCarrier) ModelParams(context.Context, ctrlproto.ModelParamsParams) (ctrlproto.ModelParamsView, error) {
	return ctrlproto.ModelParamsView{}, nil
}
func (c *addCarrier) ModelParamsSet(context.Context, ctrlproto.ModelParamsSetParams) error {
	return nil
}
func (c *addCarrier) ModelParamsReset(context.Context, ctrlproto.ModelParamsParams) error { return nil }
func (c *addCarrier) ModelAdd(_ context.Context, p ctrlproto.ModelAddParams) error {
	if c.refuse != nil {
		return c.refuse
	}
	c.added = append(c.added, p)
	return nil
}

func newAddCarrier() *addCarrier { return &addCarrier{fakeCarrier: newFakeCarrier()} }

// 🪤 The add commits through the DAEMON, not through a local models.json write
// the way applyModelEdit does.
//
// Two of the four guards cannot be checked in the TUI at all: whether the id
// already resolves, and whether the provider is one this machine can reach.
// A local write would skip both, and re-implementing them here would be the
// second copy of a predicate that has already drifted once.
func TestAddingAModelGoesThroughTheCarrier(t *testing.T) {
	c := newAddCarrier()
	i := &Interactive{cfg: InteractiveConfig{Carrier: c, CarrierSession: "s1"}}

	i.applyModelAdd("workshop", "invented-local", provider.UserModel{
		ID: "invented-local", ContextWindow: 262144, MaxTokens: 8192,
	})

	if len(c.added) != 1 {
		t.Fatalf("the carrier saw %d adds, want 1: the TUI wrote models.json behind the daemon's back", len(c.added))
	}
	got := c.added[0]
	if got.Provider != "workshop" || got.Model != "invented-local" {
		t.Errorf("sent %s/%s, want workshop/invented-local", got.Provider, got.Model)
	}
	// The whole form crosses, assembled back out of the entry by the registry.
	if got.Values["contextWindow"] != "262144" {
		t.Errorf("contextWindow crossed as %q, want 262144", got.Values["contextWindow"])
	}
	if got.Values["maxTokens"] != "8192" {
		t.Errorf("maxTokens crossed as %q, want 8192", got.Values["maxTokens"])
	}

	ok, errText := statusOf(t, i)
	if errText != "" {
		t.Errorf("statusErr = %q after a successful add", errText)
	}
	if ok == "" {
		t.Error("a successful add said nothing")
	}
}

// 🪤 The same silent-failure shape #907 fixed for delete: a refusal must not
// come back as success. Here the daemon owns the refusals the form cannot make
// itself, so reporting "added" over one would claim a model that does not exist.
func TestARefusedAddIsNotReportedAsSuccess(t *testing.T) {
	c := newAddCarrier()
	c.refuse = errors.New("model \"claude-x\" already exists under \"anthropic\", edit it instead")
	i := &Interactive{cfg: InteractiveConfig{Carrier: c, CarrierSession: "s1"}}

	i.applyModelAdd("anthropic", "claude-x", provider.UserModel{ID: "claude-x", ContextWindow: 1000})

	ok, errText := statusOf(t, i)
	if ok != "" {
		t.Errorf("statusOK = %q over a refused add", ok)
	}
	if errText == "" {
		t.Fatal("a refused add reported nothing at all")
	}
	// The daemon's reason has to survive to the screen, or the operator sees a
	// form that failed for no stated cause.
	if want := "already exists"; !strings.Contains(errText, want) {
		t.Errorf("statusErr = %q, want it to carry the daemon's reason (%q)", errText, want)
	}
}

// A carrier that serves no models.json surface (a replay session) must not open
// a form whose save can only fail.
func TestTheAddFormDoesNotOpenWhenTheCarrierCannotSave(t *testing.T) {
	i := &Interactive{
		cfg:             InteractiveConfig{Carrier: newFakeCarrier(), CarrierSession: "s1"},
		modelEditDialog: dialogs.NewModelEditDialog(),
	}

	i.openModelAdd("anthropic", "claude-x")

	if i.modelEditDialog.Active() {
		t.Error("the add form opened on a carrier that cannot commit it")
	}
	if _, errText := statusOf(t, i); errText == "" {
		t.Error("nothing explained why the form did not open")
	}
}
