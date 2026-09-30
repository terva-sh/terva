package ctrlproto

// Capability names what calling a verb DOES, which is a different axis from the
// [Group] it belongs to. Groups answer "which surface is this" and are the unit
// of NEGOTIATION; capabilities answer "what does it cost me to allow this" and
// are the unit of AUTHORIZATION.
//
// 🚨 The two axes cross, and that is the whole reason this file exists. A role
// cannot be expressed on groups alone:
//
//   - GroupConversation carries both `subscribe` (observe a stream) and
//     `prompt` (spend the operator's subscription).
//   - GroupSession looks read-shaped and is not: `sessions.generate_title`,
//     `sidechat.ask`, `suggest.reply`, `sessions.doctor`, `sessions.next_scene`
//     and `sessions.realize` all reach a provider.Client. A "read-only" role
//     built by handing out GroupSession would spend money.
//
// So a read-only role is not a group split — it is a capability mask. See
// docs/proposals/daemon-access-auth.md (step 2).
type Capability uint8

const (
	// CapRead observes state and changes nothing.
	CapRead Capability = 1 << iota
	// CapWrite mutates stored state: transcripts, cards, worlds, config,
	// credentials. Destructive verbs live here too — the mask says what a
	// caller may CAUSE, not how sorry they will be.
	CapWrite
	// CapSpend reaches a model or an image backend, and therefore costs money
	// against whatever credential the daemon holds. On a multi-tenant daemon
	// that credential is shared, which makes this the capability an operator
	// most wants to withhold.
	CapSpend
	// CapSteer changes or wakes a talkoot, a team of agents that runs without
	// a person at each turn. It is never alone on a verb: a steer verb is
	// write or spend as well. It is its own bit so that a caller may watch a
	// room, and even prompt its own sessions, without the power to create a
	// team, change its roster, or post to it. A session that a talkoot drives
	// is the team's, not the caller's: a write or a spend on it needs this bit
	// too (steer_session.go).
	//
	// 🚨 The bit binds a ctrlproto caller only. A member whose posture allows
	// bash can run `terva ctl` against its own daemon with the token that
	// daemon trusts, and so hold every capability. That is the room seal's
	// custody gap, and secrets-at-rest §8.15 closes it (docs/permissions.md).
	CapSteer
)

// capAll is every capability — what an unrestricted caller holds.
const capAll = CapRead | CapWrite | CapSpend | CapSteer

// readOnlyMethods observe and mutate nothing. Membership here is what makes a
// verb reachable by a read-only caller, so an entry is a claim that the handler
// touches no state AND reaches no provider.
var readOnlyMethods = map[Method]bool{
	// Classified at the 2026-09 rebase: the tier table, a session's state card
	// and the tool display list are all views.
	MethodModelTiers: true, MethodSessionState: true, MethodToolsDisplay: true,
	MethodSubscribe: true, MethodUnsubscribe: true,
	MethodSessionsList: true, MethodSessionsArchived: true,
	MethodUsageGet: true, MethodUsageSnapshot: true, MethodResetsList: true,
	MethodContextGet: true, MethodContextNode: true,
	MethodSurfacesList: true, MethodSurfaceGet: true,
	MethodFilesList:     true,
	MethodWorkflowsList: true, MethodWorkflowsGet: true,
	MethodConversationReveal: true, MethodConversationHistory: true,
	MethodSharedList: true, MethodSharedFetch: true,
	MethodAuthProviders: true,
	MethodSecretsStatus: true, MethodSecretsList: true,
	MethodModelsList: true, MethodModelParams: true, MethodModelDefaultFor: true,
	MethodCardsList: true, MethodCardsGet: true, MethodCardsExport: true,
	MethodCardsLint: true, MethodCardsHistory: true, MethodCardsRevision: true,
	MethodPersonasList: true, MethodPersonasGet: true,
	MethodBackgroundsList:  true,
	MethodUserPersonasList: true,
	MethodWorldsList:       true, MethodWorldsExport: true,
	MethodCardGroupsList: true, MethodSessionGroupsList: true,
	MethodSessionsExport: true,
	MethodI18nCatalog:    true,
	MethodReplayState:    true,
	MethodTenantsList:    true,
	MethodTalkootList:    true, MethodTalkootGet: true, MethodTalkootRoom: true,
	MethodTalkootInbox: true, MethodTalkootProposals: true, MethodTalkootTrace: true,
	// A preview writes nothing: create recomputes the roster it showed.
	MethodTalkootTemplates: true, MethodTalkootPreview: true,
}

// spendingMethods reach a model or an image backend. Membership is a claim that
// calling this verb can put a charge on the daemon's credential.
//
// 🪤 Several of these are NOT obvious from the name and were found by reading
// the handlers, not by reading the verb list: `sessions.generate_title`,
// `suggest.reply`, `suggest.next_step`, `sidechat.ask`, the three doctors, and
// `backgrounds.generate` (which reaches imagegen, not a chat model). `approve`
// and `answer` are here because they RESUME a paused turn — answering a tool
// prompt is what lets the model keep going.
//
// 🚨 `suggest.next_step` is the sharpest of them, because it is the one verb
// here NOBODY ASKS FOR: it fires on its own while the user is idle. A role that
// could call it would hold a way to spend the daemon's credential without a
// human ever pressing anything.
var spendingMethods = map[Method]bool{
	// turn.resume runs the loop again on a turn that died without a reply, so
	// it reaches the provider exactly as turn.retry does.
	MethodTurnResume: true,
	MethodPrompt:     true, MethodQueue: true, MethodCompact: true,
	MethodTurnSwipe: true, MethodTurnRetry: true, MethodTurnContinue: true,
	MethodApprove: true, MethodAnswer: true,
	MethodPostLine: true, MethodDirectTurn: true, MethodTurnAdvance: true,
	MethodCastSpeak:            true,
	MethodSessionGenerateTitle: true,
	MethodSideChatAsk:          true, MethodSuggestReply: true, MethodSuggestNextStep: true,
	MethodSessionsDoctor: true, MethodSessionsNextScene: true, MethodSessionsRealize: true,
	MethodCardsDoctor: true, MethodWorldsDoctor: true,
	MethodBackgroundGenerate: true,
	// A post wakes a member. A resume and an update release the deliveries a
	// pause or the old roster held, and each wakes a member too. An approved
	// proposal is an update.
	MethodTalkootPost: true, MethodTalkootResume: true, MethodTalkootUpdate: true,
	MethodTalkootDecide: true,
	// A kickoff wakes each member for its introduction.
	MethodTalkootKickoff: true,
	// A proposal only makes a card, but it names a person as its author, and
	// the card and the roster line show that name. Only a caller that could
	// make the change itself with talkoot.update may claim a person's name.
	MethodTalkootPropose: true,
}

// Capabilities reports what m does.
//
// 🔑 The default is the MOST restrictive answer, not the least. A verb this file
// has never heard of is treated as mutating, spending, and steering, so a new
// method is unreachable by a restricted role until someone classifies it
// deliberately. The alternative default — "probably harmless" — would mean every
// forgotten verb silently widens every role, and the forgetting is invisible.
// TestEveryMethodIsClassified makes the omission loud as well as safe.
func (m Method) Capabilities() Capability {
	var steer Capability
	if steerMethods[m] {
		steer = CapSteer
	}
	if readOnlyMethods[m] {
		return CapRead | steer
	}
	if spendingMethods[m] {
		return CapWrite | CapSpend | steer
	}
	if writeOnlyMethods[m] {
		return CapWrite | steer
	}
	return CapWrite | CapSpend | CapSteer
}

// writeOnlyMethods mutate state without reaching a provider. Listed explicitly
// rather than left to the default so the census can tell "classified as write"
// apart from "nobody has looked at it yet" — those are the same value and very
// different facts.
var writeOnlyMethods = map[Method]bool{
	// 🔑 talkoot.ref changes nothing, and it is still not read-only. It reads
	// any file in the home checkout by its path, and no read-only verb returns
	// a file's contents.
	MethodTalkootRef: true,
	// 🔑 talkoot.worker changes nothing either. It returns a worker's
	// transcript tail, which holds tool output such as a file's contents. The
	// tasks surface is no precedent: its list is scoped to the caller's own
	// session, and a talkoot's workers are in no caller's session.
	MethodTalkootWorker: true,
	// Classified at the 2026-09 rebase: the model catalog and tier edits, and a
	// composer draft, mutate config or session state and reach no provider.
	MethodModelAdd: true, MethodModelHide: true, MethodModelTiersSet: true,
	MethodModelTiersReset: true, MethodSessionSetComposer: true,
	MethodCancel: true, MethodClear: true, MethodQueueSet: true,
	MethodMessageEdit: true, MethodMessageDelete: true,
	// Write rather than spend: it ARMS the next request with a command's output
	// and reaches no provider itself. The prompt that carries it is the verb
	// that costs money, and a caller who cannot call `prompt` can arm a tail
	// nobody will ever send.
	MethodShellResult:   true,
	MethodVariantsPrune: true, MethodVariantsDrop: true,
	MethodSessionCreate: true, MethodSessionResume: true, MethodSessionFork: true,
	MethodSessionRename: true, MethodSessionDelete: true, MethodSessionArchive: true,
	MethodSessionRestore: true, MethodSessionDiscardDraft: true,
	MethodSurfaceAction:  true,
	MethodModelParamsSet: true, MethodModelParamsReset: true,
	MethodModelSwitch: true, MethodModelFavorite: true, MethodModelSetDefault: true,
	MethodSessionReasoning: true,
	MethodTrust:            true, MethodUntrust: true, MethodRestart: true,
	MethodResetsConsume: true,
	MethodCardsImport:   true, MethodCardsEdit: true, MethodCardsDuplicate: true,
	MethodCardsDelete: true, MethodCardsRestore: true, MethodCardsFavorite: true,
	MethodPersonasCreate: true, MethodPersonasEdit: true, MethodPersonasDelete: true,
	MethodBackgroundsImport: true, MethodBackgroundsDelete: true, MethodBackgroundBind: true,
	MethodNoteSet: true, MethodUserBind: true,
	MethodUserPersonaSave: true, MethodUserPersonaDelete: true, MethodUserPersonaSetDefault: true,
	MethodCastAdd: true, MethodCastRemove: true,
	MethodWorldLorePut: true, MethodWorldLoreDelete: true, MethodWorldSet: true,
	MethodWorldsSave: true, MethodWorldsDelete: true, MethodWorldsUpdate: true,
	MethodWorldsSetCharacterModel: true, MethodWorldsSetModel: true, MethodWorldsImport: true,
	MethodWorldsLorePut: true, MethodWorldsLoreDelete: true, MethodWorldsSet: true,
	MethodWorldsAddCharacter: true, MethodWorldsRemoveCharacter: true,
	MethodWorldsEditCharacter: true, MethodWorldsCreateCharacter: true,
	MethodCardGroupSave: true, MethodCardGroupDelete: true, MethodCardGroupSetMembers: true,
	MethodSessionGroupSave: true, MethodSessionGroupDelete: true, MethodSessionGroupSetMembers: true,
	MethodCardModelSet:   true,
	MethodAuthLoginStart: true, MethodAuthLoginSubmit: true, MethodAuthLoginCancel: true,
	MethodAuthLogout: true, MethodAuthEndpointRemove: true,
	MethodSecretsGrant: true, MethodSecretsRevoke: true, MethodSecretsForget: true,
	MethodSideChatOpen: true, MethodSideChatClose: true,
	MethodReplayControl: true,
	// Write, not spend: suspending an environment stops a daemon and sets a
	// flag. It costs nothing and — deliberately — destroys nothing.
	MethodTenantsSuspend: true, MethodTenantsResume: true,
	// Write, not spend: a new talkoot has nothing owed, so it wakes no one
	// until a post. A pause only holds deliveries.
	MethodTalkootCreate: true, MethodTalkootPause: true,
	// Write, not spend: a recruiter opens on a static greeting. The prompt
	// that talks to it is the verb that costs money.
	MethodTalkootRecruit: true,
}

// steerMethods change or wake a talkoot. Membership adds [CapSteer] to the
// verb's class above. It is not a fourth class, because every steer verb is
// also write or spend, and the census still wants each verb in exactly one of
// the three.
var steerMethods = map[Method]bool{
	MethodTalkootCreate: true, MethodTalkootUpdate: true, MethodTalkootPost: true,
	MethodTalkootPause: true, MethodTalkootResume: true,
	MethodTalkootPropose: true, MethodTalkootDecide: true,
	// A recruiter files roster proposals, so binding one steers the team.
	MethodTalkootRecruit: true,
	MethodTalkootKickoff: true,
}

// Permits reports whether a caller holding mask may invoke m: every capability
// the verb exercises must be one the caller holds.
func (m Method) Permits(mask Capability) bool {
	return m.Capabilities()&^mask == 0
}
