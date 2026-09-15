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
)

// capAll is every capability — what an unrestricted caller holds.
const capAll = CapRead | CapWrite | CapSpend

// readOnlyMethods observe and mutate nothing. Membership here is what makes a
// verb reachable by a read-only caller, so an entry is a claim that the handler
// touches no state AND reaches no provider.
var readOnlyMethods = map[Method]bool{
	MethodSubscribe: true, MethodUnsubscribe: true,
	MethodSessionsList: true, MethodSessionsArchived: true,
	MethodUsageGet: true, MethodUsageSnapshot: true, MethodResetsList: true,
	MethodContextGet: true, MethodContextNode: true,
	MethodSurfacesList: true, MethodSurfaceGet: true,
	MethodFilesList:     true,
	MethodWorkflowsList: true, MethodWorkflowsGet: true,
	MethodConversationReveal: true, MethodConversationHistory: true,
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
}

// spendingMethods reach a model or an image backend. Membership is a claim that
// calling this verb can put a charge on the daemon's credential.
//
// 🪤 Several of these are NOT obvious from the name and were found by reading
// the handlers, not by reading the verb list: `sessions.generate_title`,
// `suggest.reply`, `sidechat.ask`, the three doctors, and `backgrounds.generate`
// (which reaches imagegen, not a chat model). `approve` and `answer` are here
// because they RESUME a paused turn — answering a tool prompt is what lets the
// model keep going.
var spendingMethods = map[Method]bool{
	MethodPrompt: true, MethodQueue: true, MethodCompact: true,
	MethodTurnSwipe: true, MethodTurnRetry: true, MethodTurnContinue: true,
	MethodApprove: true, MethodAnswer: true,
	MethodPostLine: true, MethodDirectTurn: true, MethodTurnAdvance: true,
	MethodCastSpeak:            true,
	MethodSessionGenerateTitle: true,
	MethodSideChatAsk:          true, MethodSuggestReply: true,
	MethodSessionsDoctor: true, MethodSessionsNextScene: true, MethodSessionsRealize: true,
	MethodCardsDoctor: true, MethodWorldsDoctor: true,
	MethodBackgroundGenerate: true,
}

// Capabilities reports what m does.
//
// 🔑 The default is the MOST restrictive answer, not the least. A verb this file
// has never heard of is treated as both mutating and spending, so a newly added
// method is unreachable by a restricted role until someone classifies it
// deliberately. The alternative default — "probably harmless" — would mean every
// forgotten verb silently widens every role, and the forgetting is invisible.
// TestEveryMethodIsClassified makes the omission loud as well as safe.
func (m Method) Capabilities() Capability {
	if readOnlyMethods[m] {
		return CapRead
	}
	if spendingMethods[m] {
		return CapWrite | CapSpend
	}
	if writeOnlyMethods[m] {
		return CapWrite
	}
	return CapWrite | CapSpend
}

// writeOnlyMethods mutate state without reaching a provider. Listed explicitly
// rather than left to the default so the census can tell "classified as write"
// apart from "nobody has looked at it yet" — those are the same value and very
// different facts.
var writeOnlyMethods = map[Method]bool{
	MethodCancel: true, MethodClear: true, MethodQueueSet: true,
	MethodMessageEdit: true, MethodMessageDelete: true,
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
	MethodWorldSave: true, MethodWorldDelete: true, MethodWorldUpdate: true,
	MethodWorldSetCharacterModel: true, MethodWorldSetModel: true, MethodWorldsImport: true,
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
}

// Permits reports whether a caller holding mask may invoke m: every capability
// the verb exercises must be one the caller holds.
func (m Method) Permits(mask Capability) bool {
	return m.Capabilities()&^mask == 0
}
