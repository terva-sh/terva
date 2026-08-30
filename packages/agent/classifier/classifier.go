// Package classifier implements core.Classifier: it asks one cheap model
// whether a tool call that would otherwise prompt should be allowed.
//
// It takes a ready provider.Client and a model id rather than resolving them
// itself. That keeps the dependency pointing one way — the build package,
// which already owns credential and tier resolution, constructs this and
// injects — and it leaves the screening logic testable against a fake client
// with no config, no credentials and no network.
//
// # What this is not
//
// Not a security boundary. It judges a call on its face, and a model's
// judgement is neither sound nor reproducible. It screens an agent that is
// honestly mistaken — the wrong directory, an overeager rm, a migration that
// looked reversible — which is the common failure. Against a prompt-injected
// agent it is close to worthless, because whoever controls the agent controls
// the call being judged. See core/classifier.go for the full statement.
package classifier

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/modelreply"
	"terva.sh/terva/packages/provider"
)

// DefaultTimeout bounds one screening call.
//
// It is short on purpose. This sits in front of a human prompt, so every
// millisecond is latency a person waits through, and a slow verdict is worth
// less than a fast abstention: abstaining just shows the prompt that would
// have been shown anyway.
const DefaultTimeout = 8 * time.Second

// maxTokens caps the reply. A verdict is one small JSON object; anything
// longer is a model narrating, and the parser takes the last object anyway.
const maxTokens = 512

// Options configures a Screener.
type Options struct {
	// Client and Model are the resolved screening model. The caller is
	// expected to have resolved something CHEAP — see build.classifierModel,
	// which defaults to the weak swarm_tiers rung.
	Client provider.Client
	Model  string
	// Reasoning is the effort for the screening call. Empty means OFF, and
	// that is deliberate: a screening call wants an answer, not deliberation.
	Reasoning string
	// Timeout bounds one call; 0 means DefaultTimeout.
	Timeout time.Duration
	// Policy is optional extra site policy appended to the prompt.
	Policy string
	// Logf, when set, receives one line per abstention-by-failure. Failures
	// are otherwise invisible: an abstention looks exactly like a normal
	// prompt, so without this a permanently broken classifier is silent.
	Logf func(format string, args ...any)
}

// Screener is a core.Classifier backed by one model call.
//
// Safe for concurrent use: it holds no mutable state, and provider clients
// are themselves safe to share.
type Screener struct {
	opts Options
}

// The claim in Classify's doc, held by the compiler rather than by hope.
var _ core.Classifier = (*Screener)(nil)

// New returns a Screener, or nil when it has nothing to run on. A nil
// *Screener is not a valid core.Classifier — callers must check, because
// installing a typed-nil into an interface would produce a gate that thinks
// it has a classifier and panics on the first call.
func New(opts Options) *Screener {
	if opts.Client == nil || strings.TrimSpace(opts.Model) == "" {
		return nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Screener{opts: opts}
}

// systemPrompt is the screener's whole instruction. Classify passes it through
// i18n.P as the prompts-catalog key classifier.system, so an operator retunes
// what the screener refuses from $TERVA_HOME/locales/prompts/en.json instead of
// rebuilding the binary.
//
// 🪤 Five tokens below are PARSED, not read: the JSON field names "decision"
// and "reason", and the verdict words allow, deny and ask that Classify
// switches on. An override that translates them makes every verdict
// unparseable, and an unparseable verdict abstains, which looks exactly like a
// screener that is working. Reword the prose around those tokens and leave the
// tokens themselves in English.
const systemPrompt = `You screen tool calls for a coding agent and return one JSON verdict.

You judge the CALL ITSELF. You are not told why the agent wants it, so do not
assume a benign purpose and do not invent a malicious one.

Return exactly one JSON object, no prose around it:
{"decision":"allow|deny|ask","reason":"<one sentence>"}

decision:
  deny  - destructive, irreversible, and clearly outside what a coding task in
          this directory needs. Reserve this for calls where being wrong to
          allow is much worse than being wrong to refuse.
  ask   - plausibly intended, but consequential enough that a human should
          look. Destructive-but-scoped work belongs here.
  allow - routine coding work: reading, searching, building, testing, and
          edits within the working directory.

Weigh blast radius, not vocabulary. Deny-worthy shapes include: destruction
outside the working directory; rewriting or force-pushing shared history;
reading credentials in order to send them somewhere; piping remote content
into a shell; and disabling a protection.

reason is shown to the agent and is its only clue for finding another way, so
say what specifically bothered you, not that the call was "unsafe".`

// Classify implements core.Classifier.
//
// It never returns an error, by interface contract: every failure — no reply,
// a timeout, an unparseable verdict, a provider outage — becomes an
// abstention, so the gate has exactly three outcomes and the call lands in
// front of a human exactly as it would have without a classifier.
func (s *Screener) Classify(ctx context.Context, req core.ClassifyRequest) core.ClassifyResult {
	if s == nil {
		return core.ClassifyResult{Verdict: core.ClassifyAbstain}
	}

	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()

	sys := i18n.P("classifier.system", systemPrompt)
	if p := strings.TrimSpace(s.opts.Policy); p != "" {
		sys += "\n\n" + i18n.P("classifier.policy",
			"Additional site policy, which overrides the guidance above:\n%s", p)
	}

	var zero float32
	stream, err := s.opts.Client.Stream(ctx, provider.Request{
		Model:     s.opts.Model,
		System:    sys,
		MaxTokens: maxTokens,
		// Set explicitly, empty meaning off, so a model's own
		// DefaultReasoning cannot turn every gated call into a thinking turn.
		Reasoning:    s.opts.Reasoning,
		ReasoningSet: true,
		// Determinism matters more than variety for a gate: the same call
		// should get the same verdict twice.
		Temperature: &zero,
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: renderCall(req)}},
			Time:    time.Now(),
		}},
	})
	if err != nil {
		return s.abstain("stream: %v", err)
	}

	var sb strings.Builder
	for e := range stream {
		switch t := e.(type) {
		case provider.EventTextDelta:
			sb.WriteString(t.Delta)
		case provider.EventDone:
			if t.Err != nil {
				return s.abstain("stream: %v", t.Err)
			}
		}
	}

	obj, ok := modelreply.LastJSONObject(sb.String())
	if !ok {
		return s.abstain("no JSON object in reply %q", truncate(sb.String(), 200))
	}
	var v struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(obj), &v); err != nil {
		return s.abstain("unparseable verdict %q: %v", truncate(obj, 200), err)
	}

	switch strings.ToLower(strings.TrimSpace(v.Decision)) {
	case "deny":
		return core.ClassifyResult{Verdict: core.ClassifyDeny, Reason: strings.TrimSpace(v.Reason)}
	case "allow":
		return core.ClassifyResult{Verdict: core.ClassifyApprove, Reason: strings.TrimSpace(v.Reason)}
	case "ask":
		// "ask" is the model saying a human should look, which is exactly an
		// abstention: the gate then does what it would have anyway.
		return core.ClassifyResult{Verdict: core.ClassifyAbstain, Reason: strings.TrimSpace(v.Reason)}
	}
	return s.abstain("unknown decision %q", v.Decision)
}

// abstain records why and returns the no-opinion verdict.
func (s *Screener) abstain(format string, args ...any) core.ClassifyResult {
	if s.opts.Logf != nil {
		s.opts.Logf("classifier abstained: "+format, args...)
	}
	return core.ClassifyResult{Verdict: core.ClassifyAbstain}
}

// renderCall is the user turn: the call, as plainly as it can be put.
func renderCall(req core.ClassifyRequest) string {
	var b strings.Builder
	// WriteString rather than Fprintf: a translated template is not a constant,
	// and go vet rejects a non-constant format string.
	b.WriteString(i18n.P("classifier.call.tool", "tool: %s\n", req.Tool))
	if req.Mode != "" {
		b.WriteString(i18n.P("classifier.call.mode", "approval mode: %s\n", req.Mode))
	}
	if p := strings.TrimSpace(req.Preview); p != "" {
		b.WriteString(i18n.P("classifier.call.preview", "preview: %s\n", p))
	}
	b.WriteString(i18n.P("classifier.call.args", "arguments:\n"))
	// Re-indent the args so a hostile argument string cannot pass itself off
	// as a new section of the prompt. Hygiene, not a defence — see the package
	// comment on what this does not protect against.
	var pretty map[string]any
	if err := json.Unmarshal(req.Args, &pretty); err == nil {
		if enc, err := json.MarshalIndent(pretty, "  ", "  "); err == nil {
			b.WriteString("  " + string(enc) + "\n")
			return b.String()
		}
	}
	b.WriteString("  " + string(req.Args) + "\n")
	return b.String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
