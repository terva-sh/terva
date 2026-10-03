package core

import (
	"context"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// askNobody is an Asker for the getter tests. It is never asked.
type askNobody struct{}

func (askNobody) Ask(context.Context, []UserQuestion) ([]UserAnswer, error) { return nil, nil }

// The getters read under the lock the setters write with. A host reads the
// client for a side request on one goroutine while a model swap lands on
// another. Under -race this test fails if a getter whose value has a runtime
// setter reads without the lock: Client, Model, Reasoning, ReasoningSummary
// and Asker. The compaction policy is set only at construction, so nothing
// can race its getter, and the call here only proves it does not deadlock.
func TestGettersDoNotRaceTheSetters(t *testing.T) {
	a := newTestAgent(&noPrefillFakeClient{}, "m0", "", Registry{})
	other := &costRaceFakeClient{}

	var wg sync.WaitGroup
	const n = 200
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range n {
			if i%2 == 0 {
				a.SetClientAndModel(other, "", "m1")
			} else {
				a.SetModel("m2")
			}
			a.SetReasoning("high")
			a.ClearReasoning()
			a.SetReasoningSummary("auto")
			a.SetAsker(askNobody{})
		}
	}()
	go func() {
		defer wg.Done()
		for range n {
			_ = a.Client()
			_ = a.Model()
			_, _ = a.Reasoning()
			_ = a.ReasoningSummary()
			_ = a.Asker()
			_ = a.CompactionPolicy()
		}
	}()
	wg.Wait()
}

// Each getter returns what its setter or option last set.
func TestGettersReturnWhatWasSet(t *testing.T) {
	c := &noPrefillFakeClient{}
	policy := DefaultCompactionPolicy{Mode: func() AutoCompactMode { return AutoCompactOff }}
	a, err := New(c, "m", WithGate(AllowAll), WithReasoning(""), WithReasoningSummary("detailed"),
		WithAsker(askNobody{}), WithCompactionPolicy(policy))
	if err != nil {
		t.Fatal(err)
	}
	if a.Client() != provider.Client(c) || a.Model() != "m" {
		t.Errorf("Client, Model = %v, %q; want the constructor's", a.Client(), a.Model())
	}
	if level, set := a.Reasoning(); level != "" || !set {
		t.Errorf("Reasoning = %q, %v; want an explicit off", level, set)
	}
	if got := a.ReasoningSummary(); got != "detailed" {
		t.Errorf("ReasoningSummary = %q, want detailed", got)
	}
	if a.Asker() == nil {
		t.Error("Asker is nil, want the one WithAsker passed")
	}
	if p, ok := a.CompactionPolicy().(DefaultCompactionPolicy); !ok || p.Mode == nil {
		t.Errorf("CompactionPolicy = %#v, want the one WithCompactionPolicy passed", a.CompactionPolicy())
	}

	a.ClearReasoning()
	if _, set := a.Reasoning(); set {
		t.Error("Reasoning is still set after ClearReasoning")
	}
	a.SetAsker(nil)
	if a.Asker() != nil {
		t.Error("Asker survived SetAsker(nil)")
	}

	// No policy passed: the getter reports the engine's default, which is
	// what the engine decides with.
	plain := newTestAgent(c, "m", "", Registry{})
	if _, ok := plain.CompactionPolicy().(DefaultCompactionPolicy); !ok {
		t.Errorf("an agent built with no policy reports %T, want DefaultCompactionPolicy", plain.CompactionPolicy())
	}
}
