package core

import (
	"context"
	"encoding/json"
	"strings"

	"terva.sh/terva/packages/i18n"
)

// A screening classifier answers tool-call approvals that would otherwise
// become a human prompt. It is a THIRD axis, orthogonal to the two the
// approval mode and the sandbox already occupy: the mode decides whether a
// call runs at all, the sandbox bounds what it may touch once it does, and
// the classifier decides only WHO ANSWERS the question the mode raised —
// a person, or a model standing in for one.
//
// Keeping it an axis rather than a sixth ApprovalMode is deliberate. Which
// calls prompt is exactly what the mode already decides; a classifier mode
// would have to restate all five answers to that question and then drift
// from them. As a modifier it composes: `workspace mode (screened)` is a
// workspace session whose prompts get screened first.
//
// # This is not a security boundary
//
// It judges a call on its face, and a model's judgement is neither sound nor
// reproducible. Against an agent that is honestly mistaken — the wrong
// directory, an overeager rm, a migration that looked reversible — it helps,
// and that is the common failure. Against a prompt-injected agent it is close
// to worthless, because whoever controls the agent controls the call being
// judged. The policy rules, the confirm gate and the sandbox stay UNDERNEATH
// it, never behind it: a deny rule outranks any verdict this can produce.
type ClassifierMode string

const (
	// ClassifierOff is the default. No classifier runs, every prompt goes
	// to a person, and a session behaves exactly as it did before the
	// feature existed.
	ClassifierOff ClassifierMode = "off"

	// ClassifierScreen lets the classifier REFUSE a call but never permit
	// one. An approve verdict is discarded and the prompt happens anyway,
	// so the classifier can only ever subtract authority. That makes it
	// safe to leave on: the worst a wrong verdict costs is a call you have
	// to reissue, never a call that ran unasked.
	ClassifierScreen ClassifierMode = "screen"

	// ClassifierApprove additionally lets it ANSWER YES on the operator's
	// behalf, standing in for them completely.
	//
	// This is the dangerous half and it is why the whole feature is
	// opt-in. It grants authority a person never saw, so it is closer to
	// yolo than to workspace, and it is displayed with the same warning
	// treatment yolo gets. A project's config can never select it.
	ClassifierApprove ClassifierMode = "approve"
)

// ParseClassifierMode validates a mode string from a flag or config.
func ParseClassifierMode(s string) (ClassifierMode, error) {
	switch m := ClassifierMode(strings.ToLower(strings.TrimSpace(s))); m {
	case ClassifierOff, ClassifierScreen, ClassifierApprove:
		return m, nil
	case "":
		return ClassifierOff, nil
	}
	return "", i18n.Errorf("unknown classifier mode %q (valid: off, screen, approve)", s)
}

// Enabled reports whether a classifier should run at all.
func (m ClassifierMode) Enabled() bool {
	return m == ClassifierScreen || m == ClassifierApprove
}

// ClassifyVerdict is a classifier's answer.
type ClassifyVerdict string

const (
	// ClassifyAbstain means "no opinion": the classifier could not decide,
	// could not reach its model, or timed out. The gate then does exactly
	// what it would have done without a classifier — ask a person.
	//
	// Abstention is the failure mode for EVERY error on this path, and that
	// is a deliberate choice against failing closed. A provider blip that
	// denied real work would hand the agent something indistinguishable
	// from a policy decision, and an agent cannot tell those apart, so it
	// tries to route around it. Falling back to the human is recoverable.
	ClassifyAbstain ClassifyVerdict = "abstain"

	// ClassifyDeny refuses the call. Honoured in both screen and approve
	// mode, because refusing is the half that only ever subtracts.
	ClassifyDeny ClassifyVerdict = "deny"

	// ClassifyApprove permits the call. Honoured ONLY in ClassifierApprove;
	// in screen mode it is downgraded to an abstention.
	ClassifyApprove ClassifyVerdict = "approve"
)

// ClassifyRequest is what the classifier is asked to rule on.
//
// 🪤 There is no field here for the agent's stated REASON for the call, and
// that absence is load-bearing. A rationale supplied by the thing being
// judged is worth nothing when the thing has been compromised — it is the
// one channel an attacker fully controls — and it would invite the
// classifier to be talked round. It rules on the call, the mode that raised
// the question, and nothing the agent wrote.
type ClassifyRequest struct {
	// Tool is the tool name being gated.
	Tool string
	// Args are the call's arguments, post hook rewrites, so the classifier
	// judges what will actually run.
	Args json.RawMessage
	// Preview is the same human-readable rendering the confirm dialog shows.
	Preview string
	// Mode is the approval mode that raised this prompt. A call that
	// prompts in workspace mode is a different question from one that
	// prompts in ask mode, where everything prompts.
	Mode ApprovalMode
}

// ClassifyResult is the classifier's answer plus the sentence behind it.
type ClassifyResult struct {
	Verdict ClassifyVerdict
	// Reason is shown to the model when the verdict denies, and it is the
	// agent's only clue for finding another way. A denial with no reason
	// just gets a cosmetic retry of the same call.
	Reason string
}

// Classifier screens a tool call that the approval mode decided to prompt on.
//
// Implementations MUST NOT return an error: every failure is an abstention,
// so the gate has exactly three outcomes to handle and cannot mishandle a
// fourth. An implementation that wants its failures visible logs them itself.
//
// Implementations must also be safe for concurrent use — the agent can gate
// calls from more than one goroutine — and must honour ctx, because the gate
// hands over the calling turn's context and a cancelled turn must not keep
// spending model calls.
type Classifier interface {
	Classify(ctx context.Context, req ClassifyRequest) ClassifyResult
}
