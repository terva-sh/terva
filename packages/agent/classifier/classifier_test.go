package classifier

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// fakeClient scripts one reply and captures the request that produced it.
type fakeClient struct {
	reply     string
	streamErr error
	doneErr   error
	got       provider.Request
}

func (c *fakeClient) Name() string { return "fake" }

func (c *fakeClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.got = req // written before returning, so the test reads it race-free
	if c.streamErr != nil {
		return nil, c.streamErr
	}
	ch := make(chan provider.Event, 4)
	go func() {
		defer close(ch)
		if c.reply != "" {
			ch <- provider.EventTextDelta{Delta: c.reply}
		}
		ch <- provider.EventDone{Err: c.doneErr}
	}()
	return ch, nil
}

func screen(t *testing.T, c *fakeClient) core.ClassifyResult {
	t.Helper()
	s := New(Options{Client: c, Model: "cheap-model"})
	if s == nil {
		t.Fatal("New returned nil for a valid client+model")
	}
	return s.Classify(context.Background(), core.ClassifyRequest{
		Tool: "bash",
		Args: json.RawMessage(`{"command":"rm -rf /"}`),
		Mode: core.ApprovalWorkspace,
	})
}

func TestVerdictMapping(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  core.ClassifyVerdict
	}{
		{"deny", `{"decision":"deny","reason":"wipes the home directory"}`, core.ClassifyDeny},
		{"allow becomes approve", `{"decision":"allow"}`, core.ClassifyApprove},
		// "ask" is the model saying a human should look, which IS an
		// abstention: the gate then does exactly what it would have anyway.
		{"ask becomes abstain", `{"decision":"ask","reason":"consequential"}`, core.ClassifyAbstain},
		{"unknown vocabulary abstains", `{"decision":"maybe"}`, core.ClassifyAbstain},
		{"prose abstains", `I think that is fine, go ahead.`, core.ClassifyAbstain},
		{"empty reply abstains", ``, core.ClassifyAbstain},
		{"fenced json still parses", "```json\n{\"decision\":\"deny\",\"reason\":\"x\"}\n```", core.ClassifyDeny},
		{"narration then answer takes the last", `{"draft":1} finally {"decision":"deny","reason":"y"}`, core.ClassifyDeny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := screen(t, &fakeClient{reply: tc.reply})
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %q, want %q", got.Verdict, tc.want)
			}
		})
	}
}

// The deny reason is the agent's only clue for finding another way, so it must
// survive the round trip rather than being dropped.
func TestDenyCarriesTheReasonThrough(t *testing.T) {
	got := screen(t, &fakeClient{reply: `{"decision":"deny","reason":"wipes the home directory"}`})
	if got.Reason != "wipes the home directory" {
		t.Fatalf("reason = %q, want the model's sentence", got.Reason)
	}
}

// Every transport failure is an abstention, never a deny. A provider blip that
// denied real work would hand the agent something indistinguishable from a
// policy decision, and it would try to route around it.
func TestTransportFailuresAbstainRatherThanDeny(t *testing.T) {
	t.Run("stream error", func(t *testing.T) {
		got := screen(t, &fakeClient{streamErr: errors.New("connection refused")})
		if got.Verdict != core.ClassifyAbstain {
			t.Fatalf("verdict = %q, want abstain", got.Verdict)
		}
	})
	t.Run("mid-stream error", func(t *testing.T) {
		got := screen(t, &fakeClient{reply: `{"decision":"deny"}`, doneErr: errors.New("overloaded")})
		if got.Verdict != core.ClassifyAbstain {
			t.Fatalf("verdict = %q, want abstain even though a verdict had streamed", got.Verdict)
		}
	})
}

// A failure is otherwise invisible — an abstention looks exactly like a normal
// prompt — so a permanently broken classifier must at least be loggable.
func TestFailuresReachTheLog(t *testing.T) {
	var lines []string
	s := New(Options{
		Client: &fakeClient{streamErr: errors.New("boom")},
		Model:  "cheap-model",
		Logf:   func(f string, a ...any) { lines = append(lines, f) },
	})
	s.Classify(context.Background(), core.ClassifyRequest{Tool: "bash", Args: json.RawMessage(`{}`)})
	if len(lines) == 0 {
		t.Fatal("a transport failure produced no log line")
	}
}

// A typed-nil in an interface would give the gate a classifier it thinks is
// live and panics on, so New refuses to build one it cannot run.
func TestNewRefusesAnUnrunnableScreener(t *testing.T) {
	if New(Options{Model: "m"}) != nil {
		t.Fatal("New accepted a nil client")
	}
	if New(Options{Client: &fakeClient{}}) != nil {
		t.Fatal("New accepted an empty model")
	}
	if New(Options{Client: &fakeClient{}, Model: "   "}) != nil {
		t.Fatal("New accepted a blank model")
	}
}

func TestNilScreenerAbstains(t *testing.T) {
	var s *Screener
	if got := s.Classify(context.Background(), core.ClassifyRequest{}); got.Verdict != core.ClassifyAbstain {
		t.Fatalf("nil screener = %q, want abstain", got.Verdict)
	}
}

func TestDefaultTimeoutApplied(t *testing.T) {
	s := New(Options{Client: &fakeClient{}, Model: "m"})
	if s.opts.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %v, want the default %v", s.opts.Timeout, DefaultTimeout)
	}
}

// Reasoning must be sent EXPLICITLY off, or a model's own DefaultReasoning
// turns every gated tool call into a thinking turn — latency and money in
// front of a human prompt.
func TestReasoningIsExplicitlyOff(t *testing.T) {
	c := &fakeClient{reply: `{"decision":"allow"}`}
	screen(t, c)
	if !c.got.ReasoningSet {
		t.Fatal("ReasoningSet was false; the model's own default effort would apply")
	}
	if c.got.Reasoning != "" {
		t.Fatalf("Reasoning = %q, want empty (off)", c.got.Reasoning)
	}
	if c.got.Model != "cheap-model" {
		t.Fatalf("Model = %q, want the injected cheap model", c.got.Model)
	}
	if c.got.Temperature == nil || *c.got.Temperature != 0 {
		t.Fatal("Temperature must be pinned to 0 so the same call gets the same verdict twice")
	}
}

// A rung that asks for effort is the operator's choice and must survive.
func TestConfiguredReasoningSurvives(t *testing.T) {
	c := &fakeClient{reply: `{"decision":"allow"}`}
	s := New(Options{Client: c, Model: "m", Reasoning: "low"})
	s.Classify(context.Background(), core.ClassifyRequest{Tool: "bash", Args: json.RawMessage(`{}`)})
	if c.got.Reasoning != "low" {
		t.Fatalf("Reasoning = %q, want the configured \"low\"", c.got.Reasoning)
	}
}

func TestPromptCarriesTheCall(t *testing.T) {
	c := &fakeClient{reply: `{"decision":"allow"}`}
	screen(t, c)
	body := c.got.Messages[0].Content[0].(provider.TextBlock).Text
	for _, want := range []string{"bash", "rm -rf /", string(core.ApprovalWorkspace)} {
		if !strings.Contains(body, want) {
			t.Fatalf("prompt missing %q:\n%s", want, body)
		}
	}
}

// Site policy must actually reach the model, or configuring it is a no-op the
// operator has no way to notice.
func TestPolicyReachesTheSystemPrompt(t *testing.T) {
	c := &fakeClient{reply: `{"decision":"allow"}`}
	s := New(Options{Client: c, Model: "m", Policy: "never touch /etc"})
	s.Classify(context.Background(), core.ClassifyRequest{Tool: "bash", Args: json.RawMessage(`{}`)})
	if !strings.Contains(c.got.System, "never touch /etc") {
		t.Fatal("site policy did not reach the system prompt")
	}
}

// The system prompt is overridable through the prompts catalog
// (i18n.P("classifier.system")), and five of its tokens are parsed rather than
// read: the JSON field names and the three verdict words. Losing one costs no
// test and no error message, because Classify then abstains on every call and
// an abstention is indistinguishable from a screener that simply had no
// opinion. This guards the shipped English; an operator overlay is on its own.
func TestSystemPromptKeepsTheTokensClassifyParses(t *testing.T) {
	for _, tok := range []string{"decision", "reason", "allow", "deny", "ask"} {
		if !strings.Contains(systemPrompt, tok) {
			t.Errorf("system prompt no longer names %q, so the model is never told to emit it "+
				"and every verdict abstains silently", tok)
		}
	}
}

// Malformed args must still render something judgeable rather than asking the
// model to rule on an empty call.
func TestRenderCallSurvivesUnparseableArgs(t *testing.T) {
	out := renderCall(core.ClassifyRequest{Tool: "bash", Args: json.RawMessage(`not json`)})
	if !strings.Contains(out, "not json") {
		t.Fatalf("unparseable args were dropped:\n%s", out)
	}
}

// The JSON salvage step itself now lives in packages/modelreply and is
// tested there. What stays this package's business is that an unsalvageable
// reply ABSTAINS rather than denies — see TestAbstainsOn* above.
