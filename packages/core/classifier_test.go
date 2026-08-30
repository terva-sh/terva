package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// classifyFunc adapts a func to the Classifier interface for tests.
type classifyFunc func(req ClassifyRequest) ClassifyResult

func (f classifyFunc) Classify(_ context.Context, req ClassifyRequest) ClassifyResult {
	return f(req)
}

// alwaysAsk is a confirmer that records that it was reached and says no. Any
// test where it fires proves the call fell through to the human.
func alwaysAsk(reached *bool) Confirmer {
	return confirmFunc(func(string, string) ConfirmDecision {
		*reached = true
		return ConfirmDecision{Allow: false, Reason: "user declined"}
	})
}

func check(g *ConfirmGate, tool string) (bool, string) {
	ok, reason, _ := g.Check(context.Background(), tool, json.RawMessage(`{}`), "preview", "call-1")
	return ok, reason
}

// THE safety property of screen mode: a model's approval is not authority.
// An approve verdict must be discarded and the human asked anyway, or
// "deny-only" is a lie and screen mode silently became approve mode.
func TestScreenModeDiscardsApprovals(t *testing.T) {
	reached := false
	g := NewConfirmGate(alwaysAsk(&reached))
	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		return ClassifyResult{Verdict: ClassifyApprove}
	}), ClassifierScreen)

	ok, _ := check(g, "bash")
	if ok {
		t.Fatal("screen mode honoured an approve verdict: a model approval became a gate bypass")
	}
	if !reached {
		t.Fatal("the human was never asked; the approval was acted on instead of discarded")
	}
}

// Approve mode is the opposite half: the classifier stands in for the person
// completely, so the prompt must not happen at all.
func TestApproveModeHonoursApprovals(t *testing.T) {
	reached := false
	g := NewConfirmGate(alwaysAsk(&reached))
	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		return ClassifyResult{Verdict: ClassifyApprove}
	}), ClassifierApprove)

	ok, _ := check(g, "bash")
	if !ok {
		t.Fatal("approve mode did not honour an approve verdict")
	}
	if reached {
		t.Fatal("the human was still prompted; approve mode is meant to answer for them")
	}
}

// Denial subtracts authority, so it is honoured in both modes.
func TestDenyIsHonouredInBothModes(t *testing.T) {
	for _, mode := range []ClassifierMode{ClassifierScreen, ClassifierApprove} {
		t.Run(string(mode), func(t *testing.T) {
			reached := false
			g := NewConfirmGate(alwaysAsk(&reached))
			g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
				return ClassifyResult{Verdict: ClassifyDeny, Reason: "targets $HOME"}
			}), mode)

			ok, reason := check(g, "bash")
			if ok {
				t.Fatal("a deny verdict did not refuse the call")
			}
			if !strings.Contains(reason, "targets $HOME") {
				t.Fatalf("reason = %q, want the classifier's own sentence", reason)
			}
			if reached {
				t.Fatal("the human was prompted after a deny; the refusal should be final")
			}
		})
	}
}

// Every failure on this path is an abstention, and an abstention must land on
// the human rather than resolving the call either way.
func TestAbstainFallsThroughToTheHuman(t *testing.T) {
	reached := false
	g := NewConfirmGate(alwaysAsk(&reached))
	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		return ClassifyResult{Verdict: ClassifyAbstain}
	}), ClassifierApprove)

	if ok, _ := check(g, "bash"); ok {
		t.Fatal("an abstention allowed the call")
	}
	if !reached {
		t.Fatal("an abstention did not reach the human")
	}
}

// Off is the default and must be indistinguishable from the feature not
// existing: the classifier is never even consulted.
func TestOffNeverConsultsTheClassifier(t *testing.T) {
	called := false
	reached := false
	g := NewConfirmGate(alwaysAsk(&reached))
	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		called = true
		return ClassifyResult{Verdict: ClassifyApprove}
	}), ClassifierOff)

	check(g, "bash")
	if called {
		t.Fatal("the classifier ran while switched off")
	}
	if !reached {
		t.Fatal("the human was not asked; off must behave exactly as before")
	}
}

// Config the operator wrote outranks a guess a model made. A deny rule must
// win even when the classifier would approve, and the classifier must not
// even be consulted — the decision was already made.
func TestPolicyDenyOutranksClassifierApproval(t *testing.T) {
	called := false
	g := NewPolicyGate(&PermissionPolicy{
		Mode:  ApprovalAsk,
		Rules: []PermissionRule{{Tool: "bash", Decision: RuleDeny, Reason: "forbidden by rule"}},
	}, nil)
	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		called = true
		return ClassifyResult{Verdict: ClassifyApprove}
	}), ClassifierApprove)

	if ok, _ := check(g, "bash"); ok {
		t.Fatal("a classifier approval overrode an explicit deny rule")
	}
	if called {
		t.Fatal("the classifier was consulted despite an explicit deny rule deciding it")
	}
}

// A tool the user already answered "always" for must not spend a model call
// to be re-approved on every subsequent use.
func TestSessionGrantShortCircuitsBeforeTheClassifier(t *testing.T) {
	called := false
	g := NewConfirmGate(confirmFunc(func(string, string) ConfirmDecision {
		return ConfirmDecision{Allow: true, RememberTool: true}
	}))
	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		called = true
		return ClassifyResult{Verdict: ClassifyAbstain}
	}), ClassifierApprove)

	check(g, "bash") // first call: grants "always this tool"
	called = false
	if ok, _ := check(g, "bash"); !ok {
		t.Fatal("the remembered grant did not hold")
	}
	if called {
		t.Fatal("the classifier ran for a tool already granted for the session")
	}
}

// The no-responder case this feature exists for: no confirmer at all. Without
// a classifier that is a blanket refusal the agent cannot act on; with one,
// the call gets a real answer.
func TestHeadlessSessionIsScreenedNotBlanketRefused(t *testing.T) {
	t.Run("deny gives the classifier's reason, not the blanket message", func(t *testing.T) {
		g := NewConfirmGate(nil)
		g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
			return ClassifyResult{Verdict: ClassifyDeny, Reason: "rewrites shared history"}
		}), ClassifierScreen)

		ok, reason := check(g, "bash")
		if ok {
			t.Fatal("deny allowed the call")
		}
		if !strings.Contains(reason, "rewrites shared history") {
			t.Fatalf("reason = %q, want the classifier's sentence rather than the no-prompt refusal", reason)
		}
	})

	t.Run("approve mode can answer when nobody is there", func(t *testing.T) {
		g := NewConfirmGate(nil)
		g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
			return ClassifyResult{Verdict: ClassifyApprove}
		}), ClassifierApprove)

		if ok, _ := check(g, "bash"); !ok {
			t.Fatal("approve mode did not resolve a headless approval")
		}
	})

	t.Run("screen mode still refuses when nobody is there", func(t *testing.T) {
		g := NewConfirmGate(nil)
		g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
			return ClassifyResult{Verdict: ClassifyApprove}
		}), ClassifierScreen)

		if ok, _ := check(g, "bash"); ok {
			t.Fatal("screen mode approved headlessly; its approvals must always be discarded")
		}
	})
}

// A denial the agent cannot learn from just gets a cosmetic retry of the same
// call, so an empty reason is filled in rather than passed through.
func TestDenyWithNoReasonStillSaysSomething(t *testing.T) {
	g := NewConfirmGate(nil)
	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		return ClassifyResult{Verdict: ClassifyDeny, Reason: "   "}
	}), ClassifierScreen)

	_, reason := check(g, "bash")
	if strings.TrimSpace(reason) == "" {
		t.Fatal("a denial was returned with no reason at all")
	}
}

// The status bar reads this, so it must never advertise screening that is not
// happening.
func TestClassifierModeReportsOffWithoutAClassifier(t *testing.T) {
	var nilGate *ConfirmGate
	if got := nilGate.ClassifierMode(); got != ClassifierOff {
		t.Fatalf("nil gate = %q, want off", got)
	}

	g := NewConfirmGate(nil)
	if got := g.ClassifierMode(); got != ClassifierOff {
		t.Fatalf("fresh gate = %q, want off", got)
	}

	// A mode set with no classifier behind it must still report off.
	g.SetClassifier(nil, ClassifierApprove)
	if got := g.ClassifierMode(); got != ClassifierOff {
		t.Fatalf("mode with no classifier = %q, want off", got)
	}

	g.SetClassifier(classifyFunc(func(ClassifyRequest) ClassifyResult {
		return ClassifyResult{Verdict: ClassifyAbstain}
	}), ClassifierScreen)
	if got := g.ClassifierMode(); got != ClassifierScreen {
		t.Fatalf("installed = %q, want screen", got)
	}
}

// The classifier is told which mode raised the prompt, because a call that
// prompts in workspace mode is a different question from one in ask mode,
// where everything prompts.
func TestClassifierSeesTheApprovalMode(t *testing.T) {
	var got ApprovalMode
	g := NewPolicyGate(&PermissionPolicy{Mode: ApprovalAsk}, nil)
	g.SetClassifier(classifyFunc(func(req ClassifyRequest) ClassifyResult {
		got = req.Mode
		return ClassifyResult{Verdict: ClassifyAbstain}
	}), ClassifierScreen)

	check(g, "bash")
	if got != ApprovalAsk {
		t.Fatalf("classifier saw mode %q, want %q", got, ApprovalAsk)
	}
}

func TestParseClassifierMode(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ClassifierMode
		ok   bool
	}{
		{"off", ClassifierOff, true},
		{"screen", ClassifierScreen, true},
		{"approve", ClassifierApprove, true},
		{"  APPROVE  ", ClassifierApprove, true},
		{"", ClassifierOff, true},
		{"yes", "", false},
		{"deny", "", false},
	} {
		got, err := ParseClassifierMode(tc.in)
		if tc.ok != (err == nil) {
			t.Fatalf("ParseClassifierMode(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
		}
		if tc.ok && got != tc.want {
			t.Fatalf("ParseClassifierMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClassifierModeEnabled(t *testing.T) {
	if ClassifierOff.Enabled() {
		t.Fatal("off reported enabled")
	}
	if !ClassifierScreen.Enabled() || !ClassifierApprove.Enabled() {
		t.Fatal("screen/approve reported disabled")
	}
}
