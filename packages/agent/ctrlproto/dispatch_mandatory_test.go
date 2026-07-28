package ctrlproto

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/core"
)

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

// The mandatory WorkspaceService surface, driven through the same recorder as
// the optional controllers in dispatch_test.go.
//
// These 44 verbs were exempted there with the reason "mandatory, ungrouped": each
// sits on a singleton top-level `case`, so the group-fallthrough bug that table was
// built for genuinely cannot happen to them. That reasoning is sound — and it
// covers only ONE of the two failure modes the table catches.
//
// The other one is argument transposition at the call site, and the mandatory
// surface carries MORE of it than the optional controllers do, because
// serve.go destructures a params struct into positional arguments here:
//
//	s.svc.SwipeMessage(ctx, f.Sess, p.Epoch, *p.Index, p.Variant)  // three numbers
//	s.svc.History(ctx, f.Sess, p.Before, p.Limit, p.Epoch)         // three numbers
//	s.svc.SetDefaultModel(ctx, p.Provider, p.Model, p.Scope)       // three strings
//	s.svc.Node(ctx, f.Sess, p.ID, p.Op)                            // two strings
//
// Swap any adjacent pair and the compiler is silent, methods_complete_test.go
// still passes (the arm exists and is a singleton), and the verb ships applying
// the user's edit to the wrong message or saving the default to the wrong scope.
// That is the same class the optional half's own header calls out as its reason
// for existing; it was simply never extended to this half.
//
// Every case below sends DISTINCT field values, so a struct bound from the wrong
// verb — or two arguments in the wrong order — shows up as a mismatch rather
// than a coincidental zero-value match.

// --- positional call sites on the mandatory surface ---
//
// Each of these mirrors one destructured call in serve.go. The struct exists so
// the assertion can name the values by field, which is what makes a transposition
// legible in the failure output instead of "want 3, got 5".

type promptArgs struct {
	Text   string
	Images []Image
}

type editArgs struct {
	Epoch uint64
	Index int
	Text  string
}

type deleteArgs struct {
	Epoch uint64
	Index int
}

type swipeTurnArgs struct {
	Epoch   uint64
	Variant int
}

type swipeMessageArgs struct {
	Epoch   uint64
	Index   int
	Variant int
}

type approveArgs struct {
	CallID   string
	Decision core.ConfirmDecision
}

type answerArgs struct {
	AskID  string
	Answer core.UserAnswer
}

type historyArgs struct {
	Before int
	Limit  int
	Epoch  uint64
}

type nodeArgs struct {
	ID string
	Op string
}

type surfaceActionArgs struct {
	ID     string
	Action string
	Args   map[string]string
}

type switchModelArgs struct {
	Provider string
	Model    string
}

type favoriteArgs struct {
	Provider string
	Model    string
	On       bool
}

type setDefaultArgs struct {
	Provider string
	Model    string
	Scope    DefaultScope
}

type sideChatAskArgs struct {
	ID       string
	Prior    []SideChatTurn
	Question string
}

// --- recorder overrides for the mandatory surface ---
//
// recorder embeds *fakeSvc, which satisfies WorkspaceService with fixed return
// values for the transport tests in wire_test.go. Declaring these on recorder
// shadows the embedded ones for THIS harness only, so wire_test's round trips
// are untouched.

func (r *recorder) Prompt(_ context.Context, sess string, p PromptParams) error {
	text, images := p.Text, p.Images
	r.note("Prompt", sess, promptArgs{Text: text, Images: images})
	return nil
}

func (r *recorder) Queue(_ context.Context, sess, text string) error {
	r.note("Queue", sess, text)
	return nil
}

func (r *recorder) SetQueue(_ context.Context, sess string, texts []string) error {
	r.note("SetQueue", sess, texts)
	return nil
}

func (r *recorder) Cancel(_ context.Context, sess string) error {
	r.note("Cancel", sess, nil)
	return nil
}

func (r *recorder) Compact(_ context.Context, sess string) error {
	r.note("Compact", sess, nil)
	return nil
}

func (r *recorder) Clear(_ context.Context, sess string) error {
	r.note("Clear", sess, nil)
	return nil
}

func (r *recorder) EditMessage(_ context.Context, sess string, epoch uint64, index int, text string) error {
	r.note("EditMessage", sess, editArgs{Epoch: epoch, Index: index, Text: text})
	return nil
}

func (r *recorder) DeleteMessage(_ context.Context, sess string, epoch uint64, index int) error {
	r.note("DeleteMessage", sess, deleteArgs{Epoch: epoch, Index: index})
	return nil
}

func (r *recorder) SwipeTurn(_ context.Context, sess string, epoch uint64, variant int) error {
	r.note("SwipeTurn", sess, swipeTurnArgs{Epoch: epoch, Variant: variant})
	return nil
}

func (r *recorder) SwipeMessage(_ context.Context, sess string, epoch uint64, index, variant int) error {
	r.note("SwipeMessage", sess, swipeMessageArgs{Epoch: epoch, Index: index, Variant: variant})
	return nil
}

func (r *recorder) RetryTurn(_ context.Context, sess string, p TurnRetryParams) error {
	r.note("RetryTurn", sess, p)
	return nil
}

func (r *recorder) Approve(_ context.Context, sess, callID string, d core.ConfirmDecision) error {
	r.note("Approve", sess, approveArgs{CallID: callID, Decision: d})
	return nil
}

func (r *recorder) Answer(_ context.Context, sess, askID string, a core.UserAnswer) error {
	r.note("Answer", sess, answerArgs{AskID: askID, Answer: a})
	return nil
}

func (r *recorder) Sessions(_ context.Context) ([]SessionInfo, error) {
	r.note("Sessions", "", nil)
	return nil, nil
}

func (r *recorder) CreateSession(_ context.Context, opts CreateOpts) (SessionInfo, error) {
	r.note("CreateSession", "", opts)
	return SessionInfo{}, nil
}

func (r *recorder) ResumeSession(_ context.Context, sess string) (SessionInfo, error) {
	r.note("ResumeSession", sess, nil)
	return SessionInfo{}, nil
}

func (r *recorder) ForkSession(_ context.Context, sess string, fromIndex int) (SessionInfo, error) {
	r.note("ForkSession", sess, fromIndex)
	return SessionInfo{}, nil
}

func (r *recorder) RenameSession(_ context.Context, sess, title string) error {
	r.note("RenameSession", sess, title)
	return nil
}

func (r *recorder) GenerateSessionTitle(_ context.Context, sess string) (string, error) {
	r.note("GenerateSessionTitle", sess, nil)
	return "", nil
}

func (r *recorder) DeleteSession(_ context.Context, sess string) error {
	r.note("DeleteSession", sess, nil)
	return nil
}

func (r *recorder) Usage(_ context.Context, sess string) (core.WireUsage, error) {
	r.note("Usage", sess, nil)
	return core.WireUsage{}, nil
}

func (r *recorder) UsageSnapshot(_ context.Context, sess string, refresh bool) (UsageInfo, error) {
	r.note("UsageSnapshot", sess, refresh)
	return UsageInfo{}, nil
}

func (r *recorder) ListResets(_ context.Context, sess string) (ResetsListResult, error) {
	r.note("ListResets", sess, nil)
	return ResetsListResult{}, nil
}

func (r *recorder) ConsumeReset(_ context.Context, sess, id string) (ResetConsumeResult, error) {
	r.note("ConsumeReset", sess, id)
	return ResetConsumeResult{}, nil
}

func (r *recorder) SideChatOpen(_ context.Context, sess string) (string, error) {
	r.note("SideChatOpen", sess, nil)
	return "", nil
}

func (r *recorder) SideChatAsk(_ context.Context, sess, id string, prior []SideChatTurn, question string) (string, error) {
	r.note("SideChatAsk", sess, sideChatAskArgs{ID: id, Prior: prior, Question: question})
	return "", nil
}

func (r *recorder) SideChatClose(_ context.Context, sess, id string) error {
	r.note("SideChatClose", sess, id)
	return nil
}

func (r *recorder) Context(_ context.Context, sess string) (ContextBreakdown, error) {
	r.note("Context", sess, nil)
	return ContextBreakdown{}, nil
}

func (r *recorder) Node(_ context.Context, sess, id, op string) (ContextNode, error) {
	r.note("Node", sess, nodeArgs{ID: id, Op: op})
	return ContextNode{}, nil
}

func (r *recorder) History(_ context.Context, sess string, before, limit int, epoch uint64) (HistoryResult, error) {
	r.note("History", sess, historyArgs{Before: before, Limit: limit, Epoch: epoch})
	return HistoryResult{}, nil
}

func (r *recorder) Reveal(_ context.Context, sess string, ordinal int) (RevealResult, error) {
	r.note("Reveal", sess, ordinal)
	return RevealResult{}, nil
}

func (r *recorder) Surfaces(_ context.Context, sess string) ([]SurfaceMeta, error) {
	r.note("Surfaces", sess, nil)
	return nil, nil
}

func (r *recorder) Surface(_ context.Context, sess, id string) (Surface, error) {
	r.note("Surface", sess, id)
	return Surface{}, nil
}

func (r *recorder) SurfaceAction(_ context.Context, sess, id, action string, args map[string]string) error {
	r.note("SurfaceAction", sess, surfaceActionArgs{ID: id, Action: action, Args: args})
	return nil
}

func (r *recorder) Catalog(_ context.Context, lang string) (CatalogView, error) {
	r.note("Catalog", "", lang)
	return CatalogView{}, nil
}

func (r *recorder) ListFiles(_ context.Context, opts FilesListParams) (FilesListResult, error) {
	r.note("ListFiles", "", opts)
	return FilesListResult{}, nil
}

func (r *recorder) AuthProviders(_ context.Context) (ProvidersView, error) {
	r.note("AuthProviders", "", nil)
	return ProvidersView{}, nil
}

func (r *recorder) Models(_ context.Context, sess string) ([]ModelInfo, error) {
	r.note("Models", sess, nil)
	return nil, nil
}

func (r *recorder) SwitchModel(_ context.Context, sess, providerName, modelID string) error {
	r.note("SwitchModel", sess, switchModelArgs{Provider: providerName, Model: modelID})
	return nil
}

func (r *recorder) SetFavoriteModel(_ context.Context, provider, model string, on bool) error {
	r.note("SetFavoriteModel", "", favoriteArgs{Provider: provider, Model: model, On: on})
	return nil
}

func (r *recorder) SetDefaultModel(_ context.Context, provider, model string, scope DefaultScope) error {
	r.note("SetDefaultModel", "", setDefaultArgs{Provider: provider, Model: model, Scope: scope})
	return nil
}

func (r *recorder) Trust(_ context.Context, parent bool) error {
	r.note("Trust", "", parent)
	return nil
}

func (r *recorder) Untrust(_ context.Context) error {
	r.note("Untrust", "", nil)
	return nil
}

func (r *recorder) Restart(_ context.Context) error {
	r.note("Restart", "", nil)
	return nil
}

// mandatoryDispatchCases is appended to dispatchCases, so the mandatory surface
// runs through the same three assertions as the optional controllers: the right
// method fires, with the right params, for the right session.
//
// MethodTurnSwipe is deliberately absent: it is the one arm that routes to two
// different methods depending on the payload, which one row cannot express.
// TestTurnSwipeRoutesByIndexPresence below covers both branches.
func mandatoryDispatchCases() []dispatchCase {
	return []dispatchCase{
		// --- conversation ---
		{
			MethodPrompt,
			PromptParams{Text: "prompted", Images: []Image{{MimeType: "image/png", Data: []byte("png-bytes")}}},
			"Prompt",
			promptArgs{Text: "prompted", Images: []Image{{MimeType: "image/png", Data: []byte("png-bytes")}}},
		},
		{MethodQueue, QueueParams{Text: "queued"}, "Queue", "queued"},
		{MethodQueueSet, QueueSetParams{Texts: []string{"a", "b"}}, "SetQueue", []string{"a", "b"}},
		{MethodCancel, nil, "Cancel", nil},
		{MethodCompact, nil, "Compact", nil},
		{MethodClear, nil, "Clear", nil},

		// epoch/index/text and epoch/index: adjacent numbers, destructured.
		{
			MethodMessageEdit,
			MessageEditParams{Epoch: 3, Index: 5, Text: "edited"},
			"EditMessage",
			editArgs{Epoch: 3, Index: 5, Text: "edited"},
		},
		{
			MethodMessageDelete,
			MessageDeleteParams{Epoch: 7, Index: 11},
			"DeleteMessage",
			deleteArgs{Epoch: 7, Index: 11},
		},
		{
			MethodTurnRetry,
			TurnRetryParams{Epoch: 13, Guidance: "steer"},
			"RetryTurn",
			TurnRetryParams{Epoch: 13, Guidance: "steer"},
		},
		{
			MethodApprove,
			ApproveParams{CallID: "call-1", Decision: Decision{Allow: true, Reason: "ok"}},
			"Approve",
			approveArgs{CallID: "call-1", Decision: core.ConfirmDecision{Allow: true, Reason: "ok"}},
		},
		{
			MethodAnswer,
			AnswerParams{AskID: "ask-1", Answer: Answer{Answer: "yes"}},
			"Answer",
			answerArgs{AskID: "ask-1", Answer: core.UserAnswer{Answer: "yes"}},
		},

		// --- session ---
		{MethodSessionsList, nil, "Sessions", nil},
		{
			MethodSessionCreate,
			CreateOpts{Title: "new session"},
			"CreateSession",
			CreateOpts{Title: "new session"},
		},
		{MethodSessionResume, nil, "ResumeSession", nil},
		{MethodSessionFork, SessionForkParams{FromIndex: 17}, "ForkSession", 17},
		{MethodSessionRename, RenameParams{Title: "renamed"}, "RenameSession", "renamed"},
		{MethodSessionGenerateTitle, nil, "GenerateSessionTitle", nil},
		{MethodSessionDelete, nil, "DeleteSession", nil},
		{MethodUsageGet, nil, "Usage", nil},
		{MethodUsageSnapshot, UsageSnapshotParams{Refresh: true}, "UsageSnapshot", true},
		{MethodResetsList, nil, "ListResets", nil},
		{MethodResetsConsume, ResetConsumeParams{ID: "credit-9"}, "ConsumeReset", "credit-9"},
		{MethodSideChatOpen, nil, "SideChatOpen", nil},
		{
			MethodSideChatAsk,
			SideChatAskParams{ID: "sc-1", Question: "why?"},
			"SideChatAsk",
			sideChatAskArgs{ID: "sc-1", Question: "why?"},
		},
		{MethodSideChatClose, SideChatCloseParams{ID: "sc-2"}, "SideChatClose", "sc-2"},
		{MethodContextGet, nil, "Context", nil},
		{
			MethodContextNode,
			ContextNodeParams{ID: "node-1", Op: "expand"},
			"Node",
			nodeArgs{ID: "node-1", Op: "expand"},
		},
		{MethodConversationReveal, RevealParams{Ordinal: -1}, "Reveal", -1},
		{
			MethodConversationHistory,
			HistoryParams{Before: 40, Limit: 20, Epoch: 6},
			"History",
			historyArgs{Before: 40, Limit: 20, Epoch: 6},
		},
		{MethodSurfacesList, nil, "Surfaces", nil},
		{MethodSurfaceGet, SurfaceGetParams{ID: "surface-1"}, "Surface", "surface-1"},
		{
			MethodSurfaceAction,
			SurfaceActionParams{ID: "surface-2", Action: "key", Args: map[string]string{"key": "j"}},
			"SurfaceAction",
			surfaceActionArgs{ID: "surface-2", Action: "key", Args: map[string]string{"key": "j"}},
		},
		{MethodI18nCatalog, I18nCatalogParams{Lang: "fi"}, "Catalog", "fi"},
		{
			MethodFilesList,
			FilesListParams{Dir: "pkg", Recursive: true},
			"ListFiles",
			FilesListParams{Dir: "pkg", Recursive: true},
		},
		{MethodAuthProviders, nil, "AuthProviders", nil},

		// --- control ---
		{MethodModelsList, nil, "Models", nil},
		{
			MethodModelSwitch,
			ModelSwitchParams{Provider: "anthropic", Model: "opus"},
			"SwitchModel",
			switchModelArgs{Provider: "anthropic", Model: "opus"},
		},
		{
			MethodModelFavorite,
			FavoriteParams{Provider: "openai", Model: "gpt", On: true},
			"SetFavoriteModel",
			favoriteArgs{Provider: "openai", Model: "gpt", On: true},
		},
		{
			MethodModelSetDefault,
			SetDefaultParams{Provider: "gemini", Model: "flash", Scope: ScopeProject},
			"SetDefaultModel",
			setDefaultArgs{Provider: "gemini", Model: "flash", Scope: ScopeProject},
		},
		{MethodTrust, TrustParams{Parent: true}, "Trust", true},
		{MethodUntrust, nil, "Untrust", nil},
		{MethodRestart, nil, "Restart", nil},

		// --- replay ---
		{
			MethodReplayControl,
			ReplayControlParams{Action: "seek", Position: 42},
			"ReplayControl",
			ReplayControlParams{Action: "seek", Position: 42},
		},
	}
}

// turn.swipe is the only arm that picks between two service methods from the
// payload: Index absent swipes the tail span, Index present swipes the
// message-scoped variant at that position. Getting the branch backwards would
// swipe the wrong thing silently, and the dispatch table's one-row-per-verb
// shape cannot express a verb with two destinations.
//
// SwipeMessage's three trailing numbers (epoch, index, variant) are the widest
// positional call on the whole surface, which is the other half of what this
// pins.
func TestTurnSwipeRoutesByIndexPresence(t *testing.T) {
	idx := 5
	for _, tc := range []struct {
		name   string
		params TurnSwipeParams
		want   string
		args   any
	}{
		{
			name:   "no index swipes the tail span",
			params: TurnSwipeParams{Epoch: 3, Variant: 2},
			want:   "SwipeTurn",
			args:   swipeTurnArgs{Epoch: 3, Variant: 2},
		},
		{
			name:   "an index swipes that message",
			params: TurnSwipeParams{Epoch: 7, Variant: 11, Index: &idx},
			want:   "SwipeMessage",
			args:   swipeMessageArgs{Epoch: 7, Index: 5, Variant: 11},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{fakeSvc: &fakeSvc{}}
			var replies []Frame
			s := &serveState{
				svc:      rec,
				contract: allGroups(),
				write:    func(f Frame) error { replies = append(replies, f); return nil },
				subs:     map[string]context.CancelFunc{},
			}
			s.handle(context.Background(), Frame{
				Kind: KindCmd, ID: 1, Sess: dispatchSess,
				Method: MethodTurnSwipe, Params: mustJSON(t, tc.params),
			})
			for _, f := range replies {
				if f.Error != nil {
					t.Fatalf("turn.swipe answered %s: %s", f.Error.Code, f.Error.Message)
				}
			}
			if rec.called != tc.want {
				t.Fatalf("turn.swipe dispatched to %q, want %q", rec.called, tc.want)
			}
			if rec.sess != dispatchSess {
				t.Fatalf("turn.swipe forwarded session %q, want %q", rec.sess, dispatchSess)
			}
			if rec.args != tc.args {
				t.Fatalf("turn.swipe bound %#v, want %#v", rec.args, tc.args)
			}
		})
	}
}
