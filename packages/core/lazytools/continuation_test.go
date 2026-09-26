package lazytools

import (
	"context"
	"testing"

	"terva.sh/terva/packages/core"
)

// The behavior the per-provider override exists to buy: with continuation off,
// a Prompt that activated a tool group ENDS instead of continuing itself.
//
// ⚠️ Read this before writing a variant. The obvious version of this test, an
// activation driven through a TOOL call, cannot tell the two arms apart by
// request count. A tool-path activation is landed by core's
// repinActivatedVisibility at the post-tool boundary, so the group goes live
// within the same segment and the natural-stop gate has nothing left to fire
// on. Both arms then make the same number of requests, and an "off ends the
// turn" assertion passes for a reason that has nothing to do with the flag.
// That false pass is what the on-arm control below caught.
//
// The gate is the FALLBACK for a group activated off the tool path, which is
// the case with no post-tool boundary to ride. That is the only shape where the
// flag decides whether a further request happens at all, so it is the shape
// this test uses. TestActivationContinuationOffReusesPinnedTools covers the
// tool path, where the difference is what gets advertised.
func TestActivationContinuationDecidesWhetherTheTurnEnds(t *testing.T) {
	run := func(t *testing.T, on bool) (int, []string) {
		t.Helper()
		reg := core.Registry{
			"read":      &flagTool{name: "read"},
			"mail_send": extTool("mail_send", "mail"),
		}
		client := &reqCaptureClient{}
		a, v := newAgent(client, reg, core.AllowAll)
		v.SetContinuation(on)
		// Activated off the tool path, while the model produces a plain reply.
		client.onCall = func(n int) {
			if n == 1 {
				v.Activate("mail")
			}
		}
		var causes []string
		err := a.Prompt(context.Background(), "go", nil, func(ev core.AgentEvent) {
			if e, ok := ev.(core.EvContinuation); ok {
				causes = append(causes, e.Cause)
			}
		})
		if err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		return len(client.tools), causes
	}

	t.Run("off ends the turn", func(t *testing.T) {
		reqs, causes := run(t, false)
		if reqs != 1 {
			t.Errorf("want 1 request (the turn ends at the natural stop), got %d", reqs)
		}
		if len(causes) != 0 {
			t.Errorf("want no continuation, got %v", causes)
		}
	})

	// The control. Without it the off arm proves only that something did not
	// happen, which is also what a broken fixture looks like.
	t.Run("on continues the turn", func(t *testing.T) {
		reqs, causes := run(t, true)
		if reqs != 2 {
			t.Errorf("want 2 requests (natural end, then the activation continuation), got %d", reqs)
		}
		if len(causes) != 1 || causes[0] != "activation" {
			t.Errorf("want one continuation caused by the activation, got %v", causes)
		}
	})
}

// The snapshot contract, which the per-provider override rides on rather than
// changing: the flag is read ONCE per Prompt (Visibility.BeginPrompt), so a
// write mid-turn cannot mix the immediate-refresh and natural-stop-gate
// semantics inside one Prompt.
//
// This is why the override lands through the same setter as the global and
// needs no second flag threaded through the gate, and it is why a host that
// re-resolves on a provider swap takes effect on the NEXT Prompt.
func TestActivationContinuationIsSnapshotPerPrompt(t *testing.T) {
	reg := core.Registry{
		"read":      &flagTool{name: "read"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{}
	a, v := newAgent(client, reg, core.AllowAll)
	v.SetContinuation(true)

	// Activate off the tool path AND switch the feature off, both from under the
	// running Prompt. The Prompt snapshotted "on", so it must still continue.
	client.onCall = func(n int) {
		if n == 1 {
			v.Activate("mail")
			v.SetContinuation(false)
		}
	}
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if got := len(client.tools); got != 2 {
		t.Errorf("want 2 requests: the in-flight Prompt must keep the value it started with, got %d", got)
	}
	if v.ContinuationEnabled() {
		t.Error("the mid-Prompt write must still have landed, and be visible once that Prompt is over")
	}
}
