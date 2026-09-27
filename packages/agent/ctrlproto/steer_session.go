package ctrlproto

import "encoding/json"

// A session that a talkoot drives, a seated member or a recruiter, is the
// team's, and a verb that writes to it or spends on it steers the team. So such
// a verb needs [CapSteer] as well as its own class when its target session is
// bound (TKT-01M3FAJC). Reads stay open, so a caller can watch a member without
// steering it.
//
// 🔑 The rule lives here, at dispatch, rather than in each handler. The census
// in steer_session_test.go fails when a verb that acts on a session is missing
// from the tables below, so a new session verb cannot skip the rule.

// TalkootSessions is the service surface the steer rule reads. A carrier that
// runs no talkoot answers false for every session.
//
// 🔑 A carrier that does not implement it cannot say which sessions are bound,
// so a caller without steer may not write to or spend on any session there.
// The default is the restrictive answer, as it is for a verb capability.go has
// not classified: a wrapper that forgets the method refuses, and does not
// carry a write past the rule.
type TalkootSessions interface {
	// SteersTalkoot reports whether a talkoot drives the session: a session
	// seated as a member, or bound as a recruiter. An empty id names the
	// session an empty id resolves to.
	SteersTalkoot(sessID string) bool
}

// frameSessionMethods act on the session the frame names in Sess.
var frameSessionMethods = map[Method]bool{
	MethodPrompt: true, MethodQueue: true, MethodQueueSet: true, MethodCancel: true,
	MethodCompact: true, MethodClear: true, MethodMessageEdit: true, MethodMessageDelete: true,
	MethodTurnSwipe: true, MethodTurnRetry: true, MethodTurnContinue: true, MethodTurnResume: true,
	MethodApprove: true, MethodAnswer: true,
	MethodSessionResume: true, MethodSessionFork: true, MethodSessionRename: true,
	MethodSessionGenerateTitle: true, MethodSessionDelete: true,
	MethodUsageGet: true, MethodUsageSnapshot: true, MethodResetsList: true, MethodResetsConsume: true,
	MethodSideChatOpen: true, MethodSideChatAsk: true, MethodSideChatClose: true,
	MethodContextGet: true, MethodContextNode: true,
	MethodConversationReveal: true, MethodConversationHistory: true,
	MethodSurfacesList: true, MethodSurfaceGet: true, MethodSurfaceAction: true,
	MethodToolsDisplay: true, MethodModelsList: true, MethodModelSwitch: true,
	MethodSessionReasoning: true, MethodReplayControl: true, MethodReplayState: true,
	MethodSessionsDoctor: true, MethodSessionsNextScene: true, MethodSessionsRealize: true,
	MethodSessionsExport: true, MethodSuggestReply: true, MethodSuggestNextStep: true,
	MethodBackgroundGenerate: true, MethodBackgroundBind: true, MethodNoteSet: true,
	MethodShellResult: true, MethodUserBind: true, MethodSessionDiscardDraft: true,
	MethodSessionState: true, MethodSessionSetComposer: true, MethodSessionArchive: true,
	MethodSharedList: true, MethodSharedFetch: true,
	MethodCastAdd: true, MethodCastRemove: true, MethodCastSpeak: true,
	MethodWorldLorePut: true, MethodWorldLoreDelete: true, MethodWorldSet: true, MethodWorldsSave: true,
	MethodTurnAdvance: true, MethodDirectTurn: true, MethodPostLine: true,
	MethodVariantsPrune: true, MethodVariantsDrop: true,
}

// paramSessionMethods name their target session in their params, and each
// entry returns it.
var paramSessionMethods = map[Method]func(json.RawMessage) string{
	MethodSessionRestore: func(raw json.RawMessage) string {
		var p RestoreSessionParams
		_ = json.Unmarshal(raw, &p)
		return p.ID
	},
}

// steerRefusal says why f may not run for a caller without steer, or returns
// "" when the rule does not refuse it: a verb that only reads, a verb that
// names no session, or a session no talkoot drives.
func (s *serveState) steerRefusal(f Frame) string {
	if f.Method.Capabilities()&(CapWrite|CapSpend) == 0 {
		return ""
	}
	target, named := paramSessionMethods[f.Method]
	if !frameSessionMethods[f.Method] && !named {
		return ""
	}
	ts, ok := s.svc.(TalkootSessions)
	if !ok {
		return "this server cannot tell whether a talkoot drives the session"
	}
	id := f.Sess
	if named {
		// A params session that names nothing cannot be checked, so it refuses.
		if id = target(f.Params); id == "" {
			return "the params name no session to check"
		}
	}
	if ts.SteersTalkoot(id) {
		return "a talkoot drives this session"
	}
	return ""
}
