package main

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/testsupport"
)

// isolateConfig points TERVA_HOME at an empty directory so chooseModel sees no
// user swarm_tiers overrides and the built-in family tables alone decide. Without
// this the result would depend on whoever is running the tests.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
}

// The cost property, and the reason chooseModel exists: with no -model given,
// screening must NOT land on the host model when a cheaper rung resolves. This
// is the failure that would otherwise be silent and permanent — a hook wired
// the obvious way, billing host price on every gated tool call forever.
func TestDefaultsToTheCheapRungNotTheHostModel(t *testing.T) {
	isolateConfig(t)
	host := build.Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}

	model, _, err := chooseModel(host, "", false)
	if err != nil {
		t.Fatalf("chooseModel: %v", err)
	}
	if model == host.Model {
		t.Fatalf("screening resolved to the host model %q; the weak rung should have won", model)
	}
	if !strings.Contains(strings.ToLower(model), "haiku") {
		t.Fatalf("model = %q, want anthropic's weak (haiku) rung", model)
	}
}

// A provider with neither a built-in family table nor an override resolves
// nothing. Quietly using the host model there would reintroduce exactly the
// invisible per-call bill this guards against, so it must refuse and say how
// to fix it.
func TestUnresolvableTierRefusesRatherThanBillingTheHost(t *testing.T) {
	isolateConfig(t)
	gateway := build.Resolved{Provider: "openrouter", Model: "some/expensive-model"}

	_, _, err := chooseModel(gateway, "", false)
	if err == nil {
		t.Fatal("unresolvable tier was accepted; it must refuse rather than bill the host model")
	}
	for _, want := range []string{"swarm_tiers", "-host-model"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not tell the operator about %q: %v", want, err)
		}
	}
}

// The escape hatch is explicit and opt-in, matching the convention that
// anything which costs the user something stays off until they say so.
func TestHostModelIsAvailableButOnlyOnRequest(t *testing.T) {
	isolateConfig(t)
	gateway := build.Resolved{Provider: "openrouter", Model: "some/expensive-model"}

	model, _, err := chooseModel(gateway, "", true)
	if err != nil {
		t.Fatalf("chooseModel with -host-model: %v", err)
	}
	if model != gateway.Model {
		t.Fatalf("model = %q, want the host model %q", model, gateway.Model)
	}
}

// build.Resolve SUBSTITUTES an unknown model rather than failing, so an
// explicit -model that did not survive resolution must abstain: the operator
// asked for something cheap and would otherwise be billed for the fallback.
func TestExplicitModelIsHonouredStrictly(t *testing.T) {
	isolateConfig(t)

	t.Run("honoured when it survived", func(t *testing.T) {
		r := build.Resolved{Provider: "anthropic", Model: "claude-haiku-4-5"}
		model, _, err := chooseModel(r, "claude-haiku-4-5", false)
		if err != nil {
			t.Fatalf("chooseModel: %v", err)
		}
		if model != "claude-haiku-4-5" {
			t.Fatalf("model = %q, want the requested one", model)
		}
	})

	t.Run("refused when it was substituted", func(t *testing.T) {
		r := build.Resolved{Provider: "anthropic", Model: "claude-sonnet-4-5"}
		if _, _, err := chooseModel(r, "haiku-typo", false); err == nil {
			t.Fatal("a substituted model was accepted; the fallback would be billed silently")
		}
	})
}

// The deny-only posture is the whole safety argument for leaving this hook
// switched on, so it gets tested as a property rather than as an example: for
// EVERY verdict a model can produce, the default posture must never emit the
// one decision that skips the confirm gate.
func TestDefaultPostureNeverGrantsAuthority(t *testing.T) {
	for _, dec := range []string{"allow", "ALLOW", " Allow ", "deny", "ask", "", "yes", "approve", "garbage"} {
		reply, emitted := decide(verdict{Decision: dec, Reason: "r"}, false)
		if emitted && reply.Decision == "allow" {
			t.Fatalf("decision %q emitted allow in deny-only mode: a model approval became a gate bypass", dec)
		}
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name      string
		v         verdict
		allowMode bool
		want      string // "" means no opinion (nothing written)
	}{
		{"deny is emitted", verdict{Decision: "deny", Reason: "rm outside cwd"}, false, "deny"},
		{"ask is emitted", verdict{Decision: "ask", Reason: "destructive"}, false, "ask"},
		{"allow is silence unless opted in", verdict{Decision: "allow"}, false, ""},
		{"allow is emitted when opted in", verdict{Decision: "allow"}, true, "allow"},
		{"deny still emitted when opted in", verdict{Decision: "deny", Reason: "x"}, true, "deny"},
		{"case and space tolerated", verdict{Decision: "  DENY "}, false, "deny"},
		{"unknown vocabulary is no opinion", verdict{Decision: "maybe"}, true, ""},
		{"empty is no opinion", verdict{Decision: ""}, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reply, emitted := decide(tc.v, tc.allowMode)
			if tc.want == "" {
				if emitted {
					t.Fatalf("emitted %+v, want no opinion", reply)
				}
				return
			}
			if !emitted {
				t.Fatalf("no opinion, want %q", tc.want)
			}
			if reply.Decision != tc.want {
				t.Fatalf("decision = %q, want %q", reply.Decision, tc.want)
			}
		})
	}
}

// A denial with no reason teaches the agent nothing and it will retry a
// cosmetic variation of the same call, so the reason is never allowed to be
// empty on the one decision that stops work.
func TestDenyAlwaysCarriesAReason(t *testing.T) {
	reply, emitted := decide(verdict{Decision: "deny", Reason: "   "}, false)
	if !emitted {
		t.Fatal("deny was not emitted")
	}
	if strings.TrimSpace(reply.Reason) == "" {
		t.Fatal("deny emitted with an empty reason")
	}
}

func TestLastJSONObject(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"bare object", `{"decision":"deny"}`, `{"decision":"deny"}`, true},
		{
			"prose before and after",
			"Let me think.\n{\"decision\":\"ask\"}\nHope that helps!",
			`{"decision":"ask"}`,
			true,
		},
		{
			"fenced",
			"```json\n{\"decision\":\"allow\"}\n```",
			`{"decision":"allow"}`,
			true,
		},
		{
			"narration then answer takes the last",
			`{"draft":"maybe"} finally: {"decision":"deny"}`,
			`{"decision":"deny"}`,
			true,
		},
		{
			"brace inside a string does not terminate",
			`{"decision":"deny","reason":"literal } brace"}`,
			`{"decision":"deny","reason":"literal } brace"}`,
			true,
		},
		{
			"escaped quote inside a string",
			`{"decision":"deny","reason":"he said \"no\" firmly"}`,
			`{"decision":"deny","reason":"he said \"no\" firmly"}`,
			true,
		},
		{
			"nested object",
			`{"decision":"ask","meta":{"n":{"deep":1}}}`,
			`{"decision":"ask","meta":{"n":{"deep":1}}}`,
			true,
		},
		{"no object", "I refuse to answer in JSON.", "", false},
		{"unterminated", `{"decision":"deny"`, "", false},
		{"stray closer then a real object", `} {"decision":"ask"}`, `{"decision":"ask"}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lastJSONObject(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.ok, got)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseVerdict(t *testing.T) {
	v, err := parseVerdict("Here is my call.\n```json\n{\"decision\":\"deny\",\"risk\":\"high\",\"reason\":\"targets $HOME\"}\n```")
	if err != nil {
		t.Fatalf("parseVerdict: %v", err)
	}
	if v.Decision != "deny" || v.Risk != "high" || v.Reason != "targets $HOME" {
		t.Fatalf("got %+v", v)
	}
}

// A model that answers in prose must fail loudly enough for the caller to
// abstain, never silently look like a verdict.
func TestParseVerdictRejectsProse(t *testing.T) {
	if _, err := parseVerdict("I think that command is fine, go ahead."); err == nil {
		t.Fatal("prose parsed as a verdict")
	}
}

func TestRenderCallIncludesToolAndCWD(t *testing.T) {
	out := renderCall(preEvent{
		Event: "pre_tool_use",
		Tool:  "bash",
		Args:  []byte(`{"command":"rm -rf /"}`),
		CWD:   "/work/repo",
	})
	for _, want := range []string{"bash", "/work/repo", "rm -rf /"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered call missing %q:\n%s", want, out)
		}
	}
}

// Malformed args must still render something the model can judge, rather than
// dropping the payload and asking it to rule on an empty call.
func TestRenderCallSurvivesUnparseableArgs(t *testing.T) {
	out := renderCall(preEvent{Tool: "bash", Args: []byte(`not json`), CWD: "/w"})
	if !strings.Contains(out, "not json") {
		t.Fatalf("unparseable args were dropped:\n%s", out)
	}
}
