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
	MethodUsageSnapshot Method = "usage.snapshot"  // params UsageSnapshotParams, result UsageSnapshotResult (sess in frame)
	MethodContextGet    Method = "context.get"     // result ContextResult (sess in frame)
	MethodContextNode   Method = "context.node"    // params ContextNodeParams, result ContextNodeResult (sess in frame)
	MethodSurfacesList  Method = "surfaces.list"   // result SurfacesResult (sess in frame)
	MethodSurfaceGet    Method = "surface.get"     // params SurfaceGetParams, result SurfaceResult
	MethodSurfaceAction Method = "surface.action"  // params SurfaceActionParams (sess in frame)
	MethodI18nCatalog   Method = "i18n.catalog"    // params I18nCatalogParams, result I18nCatalogResult (session-independent)

	// Side chat: an ephemeral, tool-less completion against a FROZEN snapshot of
	// a session, leaving no trace in its transcript. Backs the /btw overlay.
	MethodSideChatOpen  Method = "sidechat.open"  // result SideChatOpenResult (sess in frame)
	MethodSideChatAsk   Method = "sidechat.ask"   // params SideChatAskParams, result SideChatAskResult
	MethodSideChatClose Method = "sidechat.close" // params SideChatCloseParams

	// --- control group ---

	MethodModelsList    Method = "models.list"     // result ModelsResult
	MethodModelSwitch   Method = "models.switch"   // params ModelSwitchParams (sess in frame)
	MethodModelFavorite Method = "models.favorite" // params FavoriteParams
	MethodTrust         Method = "control.trust"   // params TrustParams; grant Workspace Trust to cwd
	MethodUntrust       Method = "control.untrust" // no params; revoke Workspace Trust for cwd
	MethodRestart       Method = "control.restart" // no params; re-execs the daemon (Tier-1 self-restart)

	// --- replay group (optional; served only by a ReplayController) ---

	MethodReplayControl Method = "replay.control" // params ReplayControlParams, result ReplayStateResult (sess in frame)
	MethodReplayState   Method = "replay.state"   // result ReplayStateResult (sess in frame)
)

// Group returns the method group m belongs to, or "" if unknown.
func (m Method) Group() Group {
	switch m {
	case MethodPrompt, MethodQueue, MethodQueueSet, MethodCancel, MethodCompact,
		MethodClear, MethodApprove, MethodAnswer, MethodSubscribe, MethodUnsubscribe:
		return GroupConversation
	case MethodSessionsList, MethodSessionCreate, MethodSessionResume,
		MethodSessionRename, MethodSessionDelete, MethodUsageGet, MethodUsageSnapshot, MethodContextGet,
		MethodContextNode, MethodSurfacesList, MethodSurfaceGet, MethodSurfaceAction, MethodI18nCatalog,
		MethodSideChatOpen, MethodSideChatAsk, MethodSideChatClose:
		return GroupSession
	case MethodModelsList, MethodModelSwitch, MethodModelFavorite,
		MethodTrust, MethodUntrust, MethodRestart:
		return GroupControl
	case MethodReplayControl, MethodReplayState:
		return GroupReplay
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

// ModelSwitchParams is the payload of [MethodModelSwitch]. Provider qualifies
// Model (ids are not globally unique across providers); empty resolves across
// all providers, preserving the old wire behavior.
type ModelSwitchParams struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model"`
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

// UsageSnapshotParams is the payload of [MethodUsageSnapshot]. Refresh=true
// pulls from the provider's usage endpoint and blocks on the fetch.
type UsageSnapshotParams struct {
	Refresh bool `json:"refresh,omitempty"`
}

// UsageSnapshotResult is the payload of a [MethodUsageSnapshot] response.
type UsageSnapshotResult struct {
	Usage UsageInfo `json:"usage"`
}

// --- side chat ---

// SideChatTurn is one completed question/answer exchange inside a side chat.
// Carried on every [MethodSideChatAsk] so the daemon holds no per-turn state:
// the frozen base is the daemon's, the conversation on top of it is the
// client's, and a failed or cancelled ask simply never becomes a turn.
type SideChatTurn struct {
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// SideChatOpenResult is the payload of a [MethodSideChatOpen] response. ID
// names the frozen snapshot for the asks that follow.
type SideChatOpenResult struct {
	ID string `json:"id"`
}

// SideChatAskParams is the payload of [MethodSideChatAsk]. The daemon builds
// the request as: the frozen system prompt, the frozen transcript, then Prior
// (oldest first), then Question. No tools — a side chat is conversational.
type SideChatAskParams struct {
	ID       string         `json:"id"`
	Prior    []SideChatTurn `json:"prior,omitempty"`
	Question string         `json:"question"`
}

// SideChatAskResult is the payload of a [MethodSideChatAsk] response.
type SideChatAskResult struct {
	Text string `json:"text"`
}

// SideChatCloseParams is the payload of [MethodSideChatClose]: release the
// frozen snapshot. Closing an unknown id is not an error — a client that
// crashed and reconnected must not have to reason about what it leaked.
type SideChatCloseParams struct {
	ID string `json:"id"`
}

// ContextResult is the payload of a [MethodContextGet] response.
type ContextResult struct {
	Breakdown ContextBreakdown `json:"breakdown"`
}

// ContextNodeParams is the payload of [MethodContextNode]: resolve one context
// node by its opaque id (minted by context.get). Op selects the operation —
// empty (or "expand") returns the node with its content/children populated one
// level deep; a non-empty op is a node-named reveal (e.g. "compaction", stage 3).
type ContextNodeParams struct {
	ID string `json:"id"`
	Op string `json:"op,omitempty"`
}

// ContextNodeResult is the payload of a [MethodContextNode] response: the
// requested node with its lazily-fetched content/children filled in.
type ContextNodeResult struct {
	Node ContextNode `json:"node"`
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
