package sdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The SDK inherits the screening posture from the user's config.json the same
// way it inherits Provider and Model: a posture the machine's owner wrote down
// means the same thing in every host that runs their tools, this one included.
//
// These go through sdk.New from the outside, so a refactor that drops the
// wiring fails here rather than in somebody's embedding. No request is ever
// made — InstallClassifier builds a client, it does not call one — so a
// credential is enough to let a classifier actually install.

// classifierHome writes a scratch $TERVA_HOME whose config sets the screening
// mode. An empty mode writes no classifier block at all, which is the shipped
// shape of a config that never heard of the feature.
//
// It always writes a permission RULE as well, and that is not decoration. The
// SDK builds a gate only when the user's config produces a policy; with no
// rules and no mode override, BuildPolicy returns nil and the runtime takes
// the no-gate fast path. No gate means no screening, whatever the classifier
// block says — see TestSDKScreeningNeedsAPolicyToScreen, which pins exactly
// that. So a fixture without a rule would test the empty case forever while
// looking like it tested inheritance.
func classifierHome(t *testing.T, mode string) {
	t.Helper()
	classifierHomeWith(t, mode, []map[string]string{
		{"tool": "bash", "decision": "ask", "reason": "so a policy exists to screen for"},
	})
}

func classifierHomeWith(t *testing.T, mode string, rules []map[string]string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	cfg := map[string]any{
		"provider": "anthropic",
		"api_key":  "sk-test-not-used",
	}
	if mode != "" {
		cfg["classifier"] = map[string]any{"mode": mode}
	}
	if len(rules) > 0 {
		cfg["permissions"] = rules
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSDKClassifierPosture(t *testing.T) {
	cases := []struct {
		name       string
		userConfig string // classifier.mode in the user's config.json
		field      string // Config.Classifier
		want       core.ClassifierMode
	}{
		{
			name: "off by default, with nothing configured anywhere",
			want: core.ClassifierOff,
		},
		{
			// THE decision this wiring implements. Empty field = inherit,
			// exactly like Provider and Model.
			name:       "an empty field inherits the user's setting",
			userConfig: "screen",
			want:       core.ClassifierScreen,
		},
		{
			// The opt-out has to exist, or "inherit" would mean "cannot
			// refuse". An embedding that owns its own spend says so here.
			name:       "an explicit off opts out of inheritance",
			userConfig: "screen",
			field:      "off",
			want:       core.ClassifierOff,
		},
		{
			name:       "an explicit mode beats the user's setting",
			userConfig: "off",
			field:      "approve",
			want:       core.ClassifierApprove,
		},
		{
			name:       "a garbled mode falls back to off rather than guessing",
			userConfig: "screeeen",
			want:       core.ClassifierOff,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classifierHome(t, tc.userConfig)
			rt := newRuntime(t, Config{Classifier: tc.field})
			if got := rt.ClassifierMode(); got != tc.want {
				t.Fatalf("ClassifierMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Yolo builds NO gate at all, and the runtime still has to answer for its
// posture without dereferencing one.
//
// This is a regression guard for a real mistake: reading the mode off the gate
// unconditionally is the obvious way to write that line, and it panics on
// every yolo embedding — a nil-pointer crash in somebody else's program, on a
// path the happy tests never touch. Off is also the honest answer here: no
// gate means nothing screens, whatever the user's config asked for.
func TestSDKYoloHasNoClassifierAndDoesNotPanic(t *testing.T) {
	classifierHome(t, "approve")

	rt := newRuntime(t, Config{Yolo: true})

	if got := rt.ClassifierMode(); got != core.ClassifierOff {
		t.Fatalf("ClassifierMode() = %q under Yolo, want off: a yolo run builds no gate to screen with", got)
	}
}

// Screening needs something to screen. A config that turns the classifier on
// but writes no permission rules produces no policy, so the SDK takes its
// no-gate fast path and nothing is ever gated — there is no prompt for a
// classifier to stand in front of.
//
// Off is the honest report here rather than a bug: the classifier's seam sits
// where the policy decided to PROMPT, so even a synthetic allow-everything
// policy would screen nothing. Worth pinning because the opposite is the
// intuitive guess, and someone will eventually "fix" this by force-building a
// gate and wonder why no verdict ever arrives.
func TestSDKScreeningNeedsAPolicyToScreen(t *testing.T) {
	classifierHomeWith(t, "screen", nil) // classifier on, no rules at all

	rt := newRuntime(t, Config{})

	if got := rt.ClassifierMode(); got != core.ClassifierOff {
		t.Fatalf("ClassifierMode() = %q with no permission rules, want off: no policy means no gate, and no gate means nothing to screen", got)
	}
}
