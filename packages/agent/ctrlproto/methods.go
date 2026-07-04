package ctrlproto

import "terva.sh/terva/packages/core"

// Method names a command carried in a [KindCmd] frame. Names are grouped by
// method group; the group a method belongs to gates whether a peer may call it
// (see [Contract]).
type Method string

const (
	// --- conversation group ---

	MethodPrompt      Method = "prompt"      // params PromptParams
	MethodQueue       Method = "queue"       // params QueueParams
	MethodQueueSet    Method = "queue.set"   // params QueueSetParams (edit/cancel pending queue)
	MethodCancel      Method = "cancel"      // no params
	MethodCompact     Method = "compact"     // no params; summarize+replace the transcript
	MethodClear       Method = "clear"       // no params; wipe the transcript (no summary)
	MethodApprove     Method = "approve"     // params ApproveParams
	MethodAnswer      Method = "answer"      // params AnswerParams
	MethodSubscribe   Method = "subscribe"   // no params; streams events for the frame's sess
	MethodUnsubscribe Method = "unsubscribe" // no params

	// --- session group ---

	MethodSessionsList  Method = "sessions.list"   // result SessionsResult
	MethodSessionCreate Method = "sessions.create" // params CreateOpts, result SessionResult
	MethodSessionResume Method = "sessions.resume" // result SessionResult (sess in frame)
	MethodSessionRename Method = "sessions.rename" // params RenameParams (sess in frame)
	MethodSessionDelete Method = "sessions.delete" // no params (sess in frame)
	MethodUsageGet      Method = "usage.get"       // result UsageResult (sess in frame)
	MethodContextGet    Method = "context.get"     // result ContextResult (sess in frame)
	MethodSurfacesList  Method = "surfaces.list"   // result SurfacesResult (sess in frame)
	MethodSurfaceGet    Method = "surface.get"     // params SurfaceGetParams, result SurfaceResult
	MethodSurfaceAction Method = "surface.action"  // params SurfaceActionParams (sess in frame)
	MethodI18nCatalog   Method = "i18n.catalog"    // params I18nCatalogParams, result I18nCatalogResult (session-independent)

	// --- control group ---

	MethodModelsList    Method = "models.list"     // result ModelsResult
	MethodModelSwitch   Method = "models.switch"   // params ModelSwitchParams (sess in frame)
	MethodModelFavorite Method = "models.favorite" // params FavoriteParams
	MethodTrust         Method = "control.trust"   // params TrustParams; grant Workspace Trust to cwd
	MethodUntrust       Method = "control.untrust" // no params; revoke Workspace Trust for cwd
	MethodRestart       Method = "control.restart" // no params; re-execs the daemon (Tier-1 self-restart)
)

// Group returns the method group m belongs to, or "" if unknown.
func (m Method) Group() Group {
	switch m {
	case MethodPrompt, MethodQueue, MethodQueueSet, MethodCancel, MethodCompact,
		MethodClear, MethodApprove, MethodAnswer, MethodSubscribe, MethodUnsubscribe:
		return GroupConversation
	case MethodSessionsList, MethodSessionCreate, MethodSessionResume,
		MethodSessionRename, MethodSessionDelete, MethodUsageGet, MethodContextGet,
		MethodSurfacesList, MethodSurfaceGet, MethodSurfaceAction, MethodI18nCatalog:
		return GroupSession
	case MethodModelsList, MethodModelSwitch, MethodModelFavorite,
		MethodTrust, MethodUntrust, MethodRestart:
		return GroupControl
	}
	return ""
}

// --- command params ---

// PromptParams is the payload of [MethodPrompt].
type PromptParams struct {
	Text   string  `json:"text"`
	Images []Image `json:"images,omitempty"`
}

// QueueParams is the payload of [MethodQueue].
type QueueParams struct {
	Text string `json:"text"`
}

// QueueSetParams is the payload of [MethodQueueSet]: the complete new pending
// queue, replacing whatever is currently queued. Used to edit or cancel queued
// messages before they are injected. An empty slice clears the queue.
type QueueSetParams struct {
	Texts []string `json:"texts"`
}

// ApproveParams is the payload of [MethodApprove].
type ApproveParams struct {
	CallID   string   `json:"call_id"`
	Decision Decision `json:"decision"`
}

// AnswerParams is the payload of [MethodAnswer].
type AnswerParams struct {
	AskID  string `json:"ask_id"`
	Answer Answer `json:"answer"`
}

// RenameParams is the payload of [MethodSessionRename].
type RenameParams struct {
	Title string `json:"title"`
}

// ModelSwitchParams is the payload of [MethodModelSwitch].
type ModelSwitchParams struct {
	Model string `json:"model"`
}

// SurfaceGetParams is the payload of [MethodSurfaceGet].
type SurfaceGetParams struct {
	ID string `json:"id"`
}

// SurfaceActionParams is the payload of [MethodSurfaceAction]: an action on a
// surface, e.g. forwarding a keypress to an extension panel. Args carries
// action-specific fields (e.g. "key"/"text" for a panel key).
type SurfaceActionParams struct {
	ID     string            `json:"id"`
	Action string            `json:"action"`
	Args   map[string]string `json:"args,omitempty"`
}

// FavoriteParams is the payload of [MethodModelFavorite].
type FavoriteParams struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	On       bool   `json:"on"`
}

// TrustParams is the payload of [MethodTrust]: Parent also trusts descendant
// directories (marking "trust everything under this parent"), matching the
// interactive /trust parent form.
type TrustParams struct {
	Parent bool `json:"parent,omitempty"`
}

// I18nCatalogParams is the payload of [MethodI18nCatalog]: an optional BCP-47
// tag, empty for the daemon's active language.
type I18nCatalogParams struct {
	Lang string `json:"lang,omitempty"`
}

// --- command results ---

// SessionsResult is the payload of a [MethodSessionsList] response.
type SessionsResult struct {
	Sessions []SessionInfo `json:"sessions"`
}

// SessionResult is the payload of a [MethodSessionCreate] / [MethodSessionResume]
// response.
type SessionResult struct {
	Session SessionInfo `json:"session"`
}

// UsageResult is the payload of a [MethodUsageGet] response.
type UsageResult struct {
	Usage core.WireUsage `json:"usage"`
}

// ModelsResult is the payload of a [MethodModelsList] response.
type ModelsResult struct {
	Models []ModelInfo `json:"models"`
}

// ContextResult is the payload of a [MethodContextGet] response.
type ContextResult struct {
	Breakdown ContextBreakdown `json:"breakdown"`
}

// SurfacesResult is the payload of a [MethodSurfacesList] response: the panes
// available for the session, for the client's surface switcher.
type SurfacesResult struct {
	Surfaces []SurfaceMeta `json:"surfaces"`
}

// SurfaceResult is the payload of a [MethodSurfaceGet] response.
type SurfaceResult struct {
	Surface Surface `json:"surface"`
}

// I18nCatalogResult is the payload of a [MethodI18nCatalog] response: the
// effective (embedded ⊕ operator overlay) web string catalog for a language,
// English-as-key. The client resolves its own strings against this, falling
// back to the English key. See [CatalogView].
type I18nCatalogResult struct {
	Catalog CatalogView `json:"catalog"`
}

// --- decision / answer wire forms ---

// Decision is the wire form of [core.ConfirmDecision] carried in
// [ApproveParams].
type Decision struct {
	Allow        bool   `json:"allow"`
	Reason       string `json:"reason,omitempty"`
	RememberTool bool   `json:"remember_tool,omitempty"`
	RememberAll  bool   `json:"remember_all,omitempty"`
	PersistTool  bool   `json:"persist_tool,omitempty"`
}

// Core converts a wire Decision to the core type.
func (d Decision) Core() core.ConfirmDecision {
	return core.ConfirmDecision{
		Allow:        d.Allow,
		Reason:       d.Reason,
		RememberTool: d.RememberTool,
		RememberAll:  d.RememberAll,
		PersistTool:  d.PersistTool,
	}
}

// DecisionFromCore converts a core decision to its wire form.
func DecisionFromCore(c core.ConfirmDecision) Decision {
	return Decision{
		Allow:        c.Allow,
		Reason:       c.Reason,
		RememberTool: c.RememberTool,
		RememberAll:  c.RememberAll,
		PersistTool:  c.PersistTool,
	}
}

// Answer is the wire form of [core.UserAnswer] carried in [AnswerParams].
type Answer struct {
	Answer   string `json:"answer,omitempty"`
	Declined bool   `json:"declined,omitempty"`
}

// Core converts a wire Answer to the core type.
func (a Answer) Core() core.UserAnswer {
	return core.UserAnswer{Answer: a.Answer, Declined: a.Declined}
}

// AnswerFromCore converts a core answer to its wire form.
func AnswerFromCore(a core.UserAnswer) Answer {
	return Answer{Answer: a.Answer, Declined: a.Declined}
}
