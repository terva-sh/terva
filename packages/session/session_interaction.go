package session

import (
	"time"

	"terva.sh/terva/packages/core"

	"terva.sh/terva/packages/core/permission"
)

// The two exchanges a live session has with a person that the transcript did
// not record: a tool call stopping at the permission prompt and the decision
// taken, and the agent asking a question and the answer given. Both happened
// as ctrlproto traffic between the daemon and its clients, and the transcript
// kept only the outcome (the tool ran, or a user message arrived), so a replay
// could show a tool call resolve but never the pause where a person decided.
// TKT-01M2Y0Y4 filed the gap while planning a terminal demo.
//
// The interval is the point. A row that only said "allowed" would replay the
// prompt and the decision in one frame, which teaches nothing; Waited is what
// lets the player hold the prompt for as long as the person did.
//
// The records carry their own JSON tags rather than embedding UserQuestion and
// UserAnswer, which have none: those two types are shaped for the RPC wire by
// other code, and a transcript row is a file format that must not move when a
// wire type does.

// PermissionRecord is one tool-approval exchange, written when the decision
// arrived. Asked is when the prompt was shown; Waited is how long the person
// took.
type PermissionRecord struct {
	CallID  string        `json:"call_id"`
	Tool    string        `json:"tool"`
	Preview string        `json:"preview,omitempty"`
	Asked   time.Time     `json:"asked"`
	Waited  time.Duration `json:"waited"`
	Allow   bool          `json:"allow"`
	Reason  string        `json:"reason,omitempty"`
	// Scope is how far an allow reached: "" for this call only, "tool" for
	// the rest of the session, "tool-saved" for a rule written to config,
	// "all" for every tool this session. It is what a replay needs to show
	// which option the person picked, not only that they said yes.
	Scope string `json:"scope,omitempty"`
}

// Permission scopes, as PermissionRecord.Scope spells them.
const (
	PermissionScopeCall      = ""
	PermissionScopeTool      = "tool"
	PermissionScopeToolSaved = "tool-saved"
	PermissionScopeAll       = "all"
)

// PermissionScopeOf names the scope a decision granted.
func PermissionScopeOf(d permission.ConfirmDecision) string {
	switch {
	case !d.Allow:
		return PermissionScopeCall
	case d.RememberAll:
		return PermissionScopeAll
	case d.PersistTool:
		return PermissionScopeToolSaved
	case d.RememberTool:
		return PermissionScopeTool
	}
	return PermissionScopeCall
}

// AskRecord is one question-set exchange, written when the answers arrived.
type AskRecord struct {
	AskID     string           `json:"ask_id"`
	Questions []RecordQuestion `json:"questions"`
	Answers   []RecordAnswer   `json:"answers"`
	Asked     time.Time        `json:"asked"`
	Waited    time.Duration    `json:"waited"`
}

// RecordQuestion is UserQuestion as the transcript stores it.
type RecordQuestion struct {
	Question    string   `json:"question"`
	Slug        string   `json:"slug,omitempty"`
	Options     []string `json:"options,omitempty"`
	Recommended []string `json:"recommended,omitempty"`
	MultiSelect bool     `json:"multi_select,omitempty"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
}

// RecordAnswer is UserAnswer as the transcript stores it.
type RecordAnswer struct {
	Answer   string   `json:"answer,omitempty"`
	Answers  []string `json:"answers,omitempty"`
	Note     string   `json:"note,omitempty"`
	Declined bool     `json:"declined,omitempty"`
}

// RecordQuestions converts a question set for the transcript.
func RecordQuestions(qs []core.UserQuestion) []RecordQuestion {
	out := make([]RecordQuestion, 0, len(qs))
	for _, q := range qs {
		out = append(out, RecordQuestion{
			Question:    q.Question,
			Slug:        q.Slug,
			Options:     q.Options,
			Recommended: q.RecommendedOptions,
			MultiSelect: q.MultiSelect,
			AllowCustom: q.AllowCustom,
		})
	}
	return out
}

// RecordAnswers converts an answer set for the transcript.
func RecordAnswers(as []core.UserAnswer) []RecordAnswer {
	out := make([]RecordAnswer, 0, len(as))
	for _, a := range as {
		out = append(out, RecordAnswer{Answer: a.Answer, Answers: a.Answers, Note: a.Note, Declined: a.Declined})
	}
	return out
}

// Questions converts a stored question set back to what a client renders.
func (r AskRecord) UserQuestions() []core.UserQuestion {
	out := make([]core.UserQuestion, 0, len(r.Questions))
	for _, q := range r.Questions {
		out = append(out, core.UserQuestion{
			Question:           q.Question,
			Slug:               q.Slug,
			Options:            q.Options,
			RecommendedOptions: q.Recommended,
			MultiSelect:        q.MultiSelect,
			AllowCustom:        q.AllowCustom,
		})
	}
	return out
}

// UserAnswers converts a stored answer set back to what a client submits.
func (r AskRecord) UserAnswers() []core.UserAnswer {
	out := make([]core.UserAnswer, 0, len(r.Answers))
	for _, a := range r.Answers {
		out = append(out, core.UserAnswer{Answer: a.Answer, Answers: a.Answers, Note: a.Note, Declined: a.Declined})
	}
	return out
}

// AppendPermission writes a permission row. Safe on a nil session, like the
// other append helpers, because a mode with no transcript still prompts.
func (s *Session) AppendPermission(rec PermissionRecord) error {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	return s.writeLine(sessionLine{Type: "permission", Permission: &rec, At: &now})
}

// AppendAsk writes an ask row.
func (s *Session) AppendAsk(rec AskRecord) error {
	if s == nil {
		return nil
	}
	now := time.Now().UTC()
	return s.writeLine(sessionLine{Type: "ask", Ask: &rec, At: &now})
}
