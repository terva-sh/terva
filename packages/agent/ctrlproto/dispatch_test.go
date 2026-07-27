package ctrlproto

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// Dispatch is hand-written: serve.go binds a params struct per verb and calls a
// controller method. Nothing had ever checked that the two agree. 96 of 101
// verbs were never dispatched in any test and 67 of 70 param structs were never
// bound, so a verb wired to the wrong struct — CardEditParams where
// CardExportParams belongs — compiled, passed, and would have failed only in
// front of a user. The eight `default:`-is-the-last-verb traps were exactly this
// bug, shipped seven times over.
//
// TestEveryMethodIsDispatchedExplicitly (methods_complete_test.go) proves each
// verb has an arm of its own. It reads the SOURCE, so it cannot see what the arm
// does. This runs the arm: one frame per verb through the real handle(), into a
// recorder that captures which controller method fired and with what, then
// asserts both.
//
// Scope is the OPTIONAL controllers — the grouped arms, where every trap lived,
// where the destructive verbs live (cards.delete, worlds.delete, world.set,
// backgrounds.bind, userpersona.set_default, cast.remove, post_line), and where
// the three positional call sites live. The mandatory WorkspaceService verbs sit
// on singleton top-level cases where the fallthrough bug cannot occur; they are
// enumerated in notCovered below with a reason each, so a NEW verb cannot join
// either set unnoticed — covered + notCovered must equal every Method constant,
// the same shape as forwarder_complete_test.go.
//
// Worth knowing what this buys, concretely: transposing the last two arguments
// of vc.DropVariant(ctx, sess, p.Epoch, p.Index, p.Variant) — both int, so the
// compiler is silent — still COMPILES and still passes both of the
// methods_complete_test.go checks. Only this table fails, naming variants.drop.

// recorder implements every optional controller and records the call. It embeds
// fakeSvc for the mandatory WorkspaceService surface, which it does not assert.
type recorder struct {
	*fakeSvc
	called string // controller method name
	sess   string
	args   any // the bound params struct, or a positional-args struct
}

func (r *recorder) note(method, sess string, args any) {
	r.called, r.sess, r.args = method, sess, args
}

// --- AuthController ---
func (r *recorder) AuthLoginStart(_ context.Context, p AuthLoginStartParams) (AuthFlowStep, error) {
	r.note("AuthLoginStart", "", p)
	return AuthFlowStep{}, nil
}
func (r *recorder) AuthLoginSubmit(_ context.Context, p AuthLoginSubmitParams) error {
	r.note("AuthLoginSubmit", "", p)
	return nil
}
func (r *recorder) AuthLoginCancel(_ context.Context, p AuthFlowRef) error {
	r.note("AuthLoginCancel", "", p)
	return nil
}
func (r *recorder) AuthLogout(_ context.Context, p AuthLogoutParams) error {
	r.note("AuthLogout", "", p)
	return nil
}
func (r *recorder) AuthEndpointRemove(_ context.Context, p AuthEndpointRemoveParams) error {
	r.note("AuthEndpointRemove", "", p)
	return nil
}

// --- BackgroundsController ---
func (r *recorder) BackgroundsList(context.Context) (BackgroundsListResult, error) {
	r.note("BackgroundsList", "", nil)
	return BackgroundsListResult{}, nil
}
func (r *recorder) BackgroundsImport(_ context.Context, p BackgroundImportParams) (BackgroundView, error) {
	r.note("BackgroundsImport", "", p)
	return BackgroundView{}, nil
}
func (r *recorder) BackgroundsDelete(_ context.Context, p BackgroundDeleteParams) error {
	r.note("BackgroundsDelete", "", p)
	return nil
}
func (r *recorder) BackgroundBind(_ context.Context, sess string, p BackgroundBindParams) error {
	r.note("BackgroundBind", sess, p)
	return nil
}
func (r *recorder) BackgroundsGenerate(_ context.Context, sess string, p BackgroundGenerateParams) (BackgroundView, error) {
	r.note("BackgroundsGenerate", sess, p)
	return BackgroundView{}, nil
}

// --- CardsController ---
func (r *recorder) CardsList(context.Context) (CardsListResult, error) {
	r.note("CardsList", "", nil)
	return CardsListResult{}, nil
}
func (r *recorder) CardsGet(_ context.Context, p CardGetParams) (CardView, error) {
	r.note("CardsGet", "", p)
	return CardView{}, nil
}
func (r *recorder) CardsImport(_ context.Context, p CardImportParams) (CardView, error) {
	r.note("CardsImport", "", p)
	return CardView{}, nil
}
func (r *recorder) CardsEdit(_ context.Context, p CardEditParams) (CardView, error) {
	r.note("CardsEdit", "", p)
	return CardView{}, nil
}
func (r *recorder) CardsDelete(_ context.Context, p CardDeleteParams) error {
	r.note("CardsDelete", "", p)
	return nil
}
func (r *recorder) CardsExport(_ context.Context, p CardExportParams) (CardExport, error) {
	r.note("CardsExport", "", p)
	return CardExport{}, nil
}
func (r *recorder) CardsLint(_ context.Context, p CardLintParams) (CardLintResult, error) {
	r.note("CardsLint", "", p)
	return CardLintResult{}, nil
}
func (r *recorder) CardFavorite(_ context.Context, p CardFavoriteParams) error {
	r.note("CardFavorite", "", p)
	return nil
}
func (r *recorder) CardsHistory(_ context.Context, p CardHistoryParams) (CardHistoryResult, error) {
	r.note("CardsHistory", "", p)
	return CardHistoryResult{}, nil
}
func (r *recorder) CardsRestore(_ context.Context, p CardRestoreParams) (CardView, error) {
	r.note("CardsRestore", "", p)
	return CardView{}, nil
}
func (r *recorder) CardsRevision(_ context.Context, p CardRevisionParams) (CardRevisionView, error) {
	r.note("CardsRevision", "", p)
	return CardRevisionView{}, nil
}

// --- CastController ---
func (r *recorder) CastAdd(_ context.Context, sess string, p CastMemberParams) error {
	r.note("CastAdd", sess, p)
	return nil
}
func (r *recorder) CastRemove(_ context.Context, sess string, p CastMemberParams) error {
	r.note("CastRemove", sess, p)
	return nil
}
func (r *recorder) CastSpeak(_ context.Context, sess string, p CastSpeakParams) error {
	r.note("CastSpeak", sess, p)
	return nil
}

// --- ContinueController / DraftController / NoteController / SuggestController / UserController ---
func (r *recorder) ContinueTurn(_ context.Context, sess string, epoch uint64) error {
	r.note("ContinueTurn", sess, continueArgs{Epoch: epoch})
	return nil
}
func (r *recorder) DiscardDraft(_ context.Context, sess string) error {
	r.note("DiscardDraft", sess, nil)
	return nil
}
func (r *recorder) NoteSet(_ context.Context, sess string, p NoteSetParams) error {
	r.note("NoteSet", sess, p)
	return nil
}
func (r *recorder) SuggestReply(_ context.Context, sess string, p SuggestParams) (SuggestResult, error) {
	r.note("SuggestReply", sess, p)
	return SuggestResult{}, nil
}
func (r *recorder) UserBind(_ context.Context, sess string, p UserBindParams) error {
	r.note("UserBind", sess, p)
	return nil
}

// --- DirectController ---
func (r *recorder) PostLine(_ context.Context, sess string, p PostLineParams) error {
	r.note("PostLine", sess, p)
	return nil
}
func (r *recorder) DirectTurn(_ context.Context, sess string, p DirectTurnParams) error {
	r.note("DirectTurn", sess, p)
	return nil
}
func (r *recorder) AdvanceTurn(_ context.Context, sess string) error {
	r.note("AdvanceTurn", sess, nil)
	return nil
}

// --- ExportController ---
func (r *recorder) SessionsExport(_ context.Context, sess string, p SessionExportParams) (SessionExport, error) {
	r.note("SessionsExport", sess, p)
	return SessionExport{}, nil
}

// --- SessionArchiveController ---
func (r *recorder) ArchiveSession(_ context.Context, sess string) (ArchivedSessionInfo, error) {
	r.note("ArchiveSession", sess, nil)
	return ArchivedSessionInfo{}, nil
}
func (r *recorder) ArchivedSessions(_ context.Context) ([]ArchivedSessionInfo, error) {
	r.note("ArchivedSessions", "", nil)
	return nil, nil
}
func (r *recorder) RestoreSession(_ context.Context, p RestoreSessionParams) (SessionInfo, error) {
	r.note("RestoreSession", "", p)
	return SessionInfo{}, nil
}

// --- WorkflowsController ---
func (r *recorder) WorkflowRuns(_ context.Context) ([]WorkflowRunInfo, error) {
	r.note("WorkflowRuns", "", nil)
	return nil, nil
}
func (r *recorder) WorkflowRun(_ context.Context, p WorkflowGetParams) (WorkflowRunView, error) {
	r.note("WorkflowRun", "", p)
	return WorkflowRunView{}, nil
}

// --- DoctorController ---
func (r *recorder) CardsDoctor(_ context.Context, p DoctorParams) (DoctorResult, error) {
	r.note("CardsDoctor", "", p)
	return DoctorResult{}, nil
}
func (r *recorder) SessionsDoctor(_ context.Context, sess string, p SessionDoctorParams) (SessionDoctorResult, error) {
	r.note("SessionsDoctor", sess, p)
	return SessionDoctorResult{}, nil
}
func (r *recorder) SessionsNextScene(_ context.Context, sess string, p NextSceneParams) (NextSceneResult, error) {
	r.note("SessionsNextScene", sess, p)
	return NextSceneResult{}, nil
}
func (r *recorder) SessionsRealize(_ context.Context, sess string, p RealizeParams) (RealizeResult, error) {
	r.note("SessionsRealize", sess, p)
	return RealizeResult{}, nil
}

// --- ModelParamsController ---
func (r *recorder) ModelParams(_ context.Context, p ModelParamsParams) (ModelParamsView, error) {
	r.note("ModelParams", "", p)
	return ModelParamsView{}, nil
}
func (r *recorder) ModelParamsSet(_ context.Context, p ModelParamsSetParams) error {
	r.note("ModelParamsSet", "", p)
	return nil
}
func (r *recorder) ModelParamsReset(_ context.Context, p ModelParamsParams) error {
	r.note("ModelParamsReset", "", p)
	return nil
}

// --- PersonasController ---
func (r *recorder) PersonasList(context.Context) (PersonasListResult, error) {
	r.note("PersonasList", "", nil)
	return PersonasListResult{}, nil
}
func (r *recorder) PersonasGet(_ context.Context, p PersonaGetParams) (PersonaView, error) {
	r.note("PersonasGet", "", p)
	return PersonaView{}, nil
}
func (r *recorder) PersonasCreate(_ context.Context, p PersonaWriteParams) (PersonaView, error) {
	r.note("PersonasCreate", "", p)
	return PersonaView{}, nil
}
func (r *recorder) PersonasEdit(_ context.Context, p PersonaWriteParams) (PersonaView, error) {
	r.note("PersonasEdit", "", p)
	return PersonaView{}, nil
}
func (r *recorder) PersonasDelete(_ context.Context, p PersonaDeleteParams) error {
	r.note("PersonasDelete", "", p)
	return nil
}

// --- ReplayController ---
func (r *recorder) ReplayControl(_ context.Context, sess string, p ReplayControlParams) (ReplayState, error) {
	r.note("ReplayControl", sess, p)
	return ReplayState{}, nil
}
func (r *recorder) ReplayState(_ context.Context, sess string) (ReplayState, error) {
	r.note("ReplayState", sess, nil)
	return ReplayState{}, nil
}

// --- UserPersonasController ---
func (r *recorder) UserPersonasList(context.Context) (UserPersonasListResult, error) {
	r.note("UserPersonasList", "", nil)
	return UserPersonasListResult{}, nil
}
func (r *recorder) UserPersonaSave(_ context.Context, p UserPersonaView) (UserPersonaView, error) {
	r.note("UserPersonaSave", "", p)
	return UserPersonaView{}, nil
}
func (r *recorder) UserPersonaDelete(_ context.Context, p UserPersonaRef) error {
	r.note("UserPersonaDelete", "", p)
	return nil
}
func (r *recorder) UserPersonaSetDefault(_ context.Context, p UserPersonaRef) error {
	r.note("UserPersonaSetDefault", "", p)
	return nil
}

// --- VariantsController (positional — the reason this test exists) ---
type variantArgs struct {
	Epoch   uint64
	Index   int
	Variant int
}
type continueArgs struct{ Epoch uint64 }

func (r *recorder) PruneVariants(_ context.Context, sess string, epoch uint64, index int) error {
	r.note("PruneVariants", sess, variantArgs{Epoch: epoch, Index: index})
	return nil
}
func (r *recorder) DropVariant(_ context.Context, sess string, epoch uint64, index, variant int) error {
	r.note("DropVariant", sess, variantArgs{Epoch: epoch, Index: index, Variant: variant})
	return nil
}

// --- WorldController ---
func (r *recorder) WorldLorePut(_ context.Context, sess string, p WorldLorePutParams) error {
	r.note("WorldLorePut", sess, p)
	return nil
}
func (r *recorder) WorldLoreDelete(_ context.Context, sess string, p WorldLoreDeleteParams) error {
	r.note("WorldLoreDelete", sess, p)
	return nil
}
func (r *recorder) WorldSet(_ context.Context, sess string, p WorldSetParams) error {
	r.note("WorldSet", sess, p)
	return nil
}
func (r *recorder) WorldsList(context.Context) (WorldsListResult, error) {
	r.note("WorldsList", "", nil)
	return WorldsListResult{}, nil
}
func (r *recorder) WorldSave(_ context.Context, sess string, p WorldSaveParams) (WorldView, error) {
	r.note("WorldSave", sess, p)
	return WorldView{}, nil
}
func (r *recorder) WorldDelete(_ context.Context, p WorldDeleteParams) error {
	r.note("WorldDelete", "", p)
	return nil
}
func (r *recorder) WorldUpdate(_ context.Context, p WorldUpdateParams) (WorldView, error) {
	r.note("WorldUpdate", "", p)
	return WorldView{}, nil
}
func (r *recorder) WorldSetCharacterModel(_ context.Context, p WorldSetCharacterModelParams) (WorldView, error) {
	r.note("WorldSetCharacterModel", "", p)
	return WorldView{}, nil
}
func (r *recorder) WorldsExport(_ context.Context, p WorldExportParams) (WorldExport, error) {
	r.note("WorldsExport", "", p)
	return WorldExport{}, nil
}
func (r *recorder) WorldsImport(_ context.Context, p WorldImportParams) (WorldView, error) {
	r.note("WorldsImport", "", p)
	return WorldView{}, nil
}
func (r *recorder) CardGroupsList(context.Context) (CardGroupsResult, error) {
	r.note("CardGroupsList", "", nil)
	return CardGroupsResult{}, nil
}
func (r *recorder) CardGroupSave(_ context.Context, p CardGroupSaveParams) (GroupView, error) {
	r.note("CardGroupSave", "", p)
	return GroupView{}, nil
}
func (r *recorder) CardGroupDelete(_ context.Context, p CardGroupDeleteParams) error {
	r.note("CardGroupDelete", "", p)
	return nil
}
func (r *recorder) CardGroupSetMembers(_ context.Context, p CardGroupSetMembersParams) (GroupView, error) {
	r.note("CardGroupSetMembers", "", p)
	return GroupView{}, nil
}
func (r *recorder) SessionGroupsList(context.Context) (SessionGroupsResult, error) {
	r.note("SessionGroupsList", "", nil)
	return SessionGroupsResult{}, nil
}
func (r *recorder) SessionGroupSave(_ context.Context, p SessionGroupSaveParams) (GroupView, error) {
	r.note("SessionGroupSave", "", p)
	return GroupView{}, nil
}
func (r *recorder) SessionGroupDelete(_ context.Context, p SessionGroupDeleteParams) error {
	r.note("SessionGroupDelete", "", p)
	return nil
}
func (r *recorder) SessionGroupSetMembers(_ context.Context, p SessionGroupSetMembersParams) (GroupView, error) {
	r.note("SessionGroupSetMembers", "", p)
	return GroupView{}, nil
}
func (r *recorder) ModelDefaultFor(_ context.Context, p DefaultForParams) (DefaultForResult, error) {
	r.note("ModelDefaultFor", "", p)
	return DefaultForResult{}, nil
}
func (r *recorder) CardModelSet(_ context.Context, p CardModelSetParams) error {
	r.note("CardModelSet", "", p)
	return nil
}

// dispatchCase is one verb, the params sent, and what must have happened.
type dispatchCase struct {
	method Method
	params any    // marshalled into the frame
	want   string // controller method that must fire
	args   any    // expected bound params (nil = don't assert)
}

const dispatchSess = "sess-42"

// Every case sends DISTINCT field values, so a struct bound from the wrong verb
// shows up as a mismatch rather than a coincidental zero-value match.
func dispatchCases() []dispatchCase {
	return []dispatchCase{
		// --- model params: default: used to fall through to Reset ---
		{MethodModelParams, ModelParamsParams{Provider: "anthropic", Model: "opus"}, "ModelParams", ModelParamsParams{Provider: "anthropic", Model: "opus"}},
		{MethodModelParamsSet, ModelParamsSetParams{Provider: "openai", Model: "gpt"}, "ModelParamsSet", nil},
		{MethodModelParamsReset, ModelParamsParams{Provider: "gemini", Model: "flash"}, "ModelParamsReset", ModelParamsParams{Provider: "gemini", Model: "flash"}},

		// --- cards: default: used to fall through to Delete ---
		{MethodCardsList, nil, "CardsList", nil},
		{MethodCardsGet, CardGetParams{ID: "kobeni"}, "CardsGet", CardGetParams{ID: "kobeni"}},
		{MethodCardsImport, CardImportParams{Path: "/imported.png"}, "CardsImport", nil},
		// Card carries a raw body: an empty RawMessage marshals to `null` and
		// returns as the four bytes "null", so send a real one and round-trip it.
		{
			MethodCardsEdit,
			CardEditParams{ID: "edited", Card: json.RawMessage(`{"name":"k"}`)},
			"CardsEdit",
			CardEditParams{ID: "edited", Card: json.RawMessage(`{"name":"k"}`)},
		},
		{MethodCardsExport, CardExportParams{ID: "exported"}, "CardsExport", CardExportParams{ID: "exported"}},
		{MethodCardsLint, CardLintParams{ID: "linted"}, "CardsLint", CardLintParams{ID: "linted"}},
		{MethodCardsDelete, CardDeleteParams{ID: "deleted"}, "CardsDelete", CardDeleteParams{ID: "deleted"}},
		{MethodCardsFavorite, CardFavoriteParams{ID: "fav", Favorite: true}, "CardFavorite", CardFavoriteParams{ID: "fav", Favorite: true}},
		{MethodCardsHistory, CardHistoryParams{ID: "historied"}, "CardsHistory", CardHistoryParams{ID: "historied"}},
		{
			MethodCardsRestore,
			CardRestoreParams{ID: "restored", Ref: "1700000000000"},
			"CardsRestore",
			CardRestoreParams{ID: "restored", Ref: "1700000000000"},
		},
		{
			MethodCardsRevision,
			CardRevisionParams{ID: "revised", Ref: "1700000000001"},
			"CardsRevision",
			CardRevisionParams{ID: "revised", Ref: "1700000000001"},
		},

		// --- personas: the arm the original incident was found on ---
		{MethodPersonasList, nil, "PersonasList", nil},
		{MethodPersonasGet, PersonaGetParams{Name: "kertoja"}, "PersonasGet", PersonaGetParams{Name: "kertoja"}},
		{MethodPersonasCreate, PersonaWriteParams{Name: "made"}, "PersonasCreate", PersonaWriteParams{Name: "made"}},
		{MethodPersonasEdit, PersonaWriteParams{Name: "changed"}, "PersonasEdit", PersonaWriteParams{Name: "changed"}},
		// The incident itself: delete must NOT arrive as an empty-bodied edit.
		{MethodPersonasDelete, PersonaDeleteParams{Name: "removed"}, "PersonasDelete", PersonaDeleteParams{Name: "removed"}},

		// --- backgrounds: default: used to fall through to Bind ---
		{MethodBackgroundsList, nil, "BackgroundsList", nil},
		{MethodBackgroundsImport, BackgroundImportParams{Path: "/bg-in.png"}, "BackgroundsImport", nil},
		{MethodBackgroundsDelete, BackgroundDeleteParams{ID: "bg-gone"}, "BackgroundsDelete", BackgroundDeleteParams{ID: "bg-gone"}},
		{MethodBackgroundBind, BackgroundBindParams{Background: "bg-bound"}, "BackgroundBind", BackgroundBindParams{Background: "bg-bound"}},
		{MethodBackgroundGenerate, BackgroundGenerateParams{Prompt: "a lantern-lit alley"}, "BackgroundsGenerate", BackgroundGenerateParams{Prompt: "a lantern-lit alley"}},

		// --- user personas: default: used to fall through to SetDefault ---
		{MethodUserPersonasList, nil, "UserPersonasList", nil},
		{MethodUserPersonaSave, UserPersonaView{Ref: "saved"}, "UserPersonaSave", nil},
		{MethodUserPersonaDelete, UserPersonaRef{Ref: "dropped"}, "UserPersonaDelete", UserPersonaRef{Ref: "dropped"}},
		{MethodUserPersonaSetDefault, UserPersonaRef{Ref: "defaulted"}, "UserPersonaSetDefault", UserPersonaRef{Ref: "defaulted"}},

		// --- cast: the else-arm used to mean Remove ---
		{MethodCastAdd, CastMemberParams{Name: "added"}, "CastAdd", CastMemberParams{Name: "added"}},
		{MethodCastRemove, CastMemberParams{Name: "removed"}, "CastRemove", CastMemberParams{Name: "removed"}},

		// --- world / worlds: two defaults, Set and Delete ---
		{MethodWorldLorePut, WorldLorePutParams{Replace: "lore-in"}, "WorldLorePut", WorldLorePutParams{Replace: "lore-in"}},
		{MethodWorldLoreDelete, WorldLoreDeleteParams{Name: "lore-out"}, "WorldLoreDelete", WorldLoreDeleteParams{Name: "lore-out"}},
		{MethodWorldSet, WorldSetParams{Coordination: "shared"}, "WorldSet", WorldSetParams{Coordination: "shared"}},
		{MethodWorldsList, nil, "WorldsList", nil},
		{MethodWorldSave, WorldSaveParams{Name: "w-saved"}, "WorldSave", WorldSaveParams{Name: "w-saved"}},
		{MethodWorldUpdate, WorldUpdateParams{ID: "w-upd", Name: "renamed"}, "WorldUpdate", WorldUpdateParams{ID: "w-upd", Name: "renamed"}},
		{MethodWorldSetCharacterModel, WorldSetCharacterModelParams{ID: "w-cm", Character: "Elira", Model: "gpt-5"}, "WorldSetCharacterModel", WorldSetCharacterModelParams{ID: "w-cm", Character: "Elira", Model: "gpt-5"}},
		{MethodWorldsExport, WorldExportParams{ID: "w-exp"}, "WorldsExport", WorldExportParams{ID: "w-exp"}},
		{MethodWorldsImport, WorldImportParams{Path: "/w-imp"}, "WorldsImport", nil},
		{MethodWorldDelete, WorldDeleteParams{ID: "w-del"}, "WorldDelete", WorldDeleteParams{ID: "w-del"}},
		{MethodCardGroupsList, nil, "CardGroupsList", nil},
		{MethodCardGroupSave, CardGroupSaveParams{Name: "cg-new"}, "CardGroupSave", CardGroupSaveParams{Name: "cg-new"}},
		{MethodCardGroupSetMembers, CardGroupSetMembersParams{ID: "cg-mem", Members: []string{"card-a"}}, "CardGroupSetMembers", CardGroupSetMembersParams{ID: "cg-mem", Members: []string{"card-a"}}},
		{MethodCardGroupDelete, CardGroupDeleteParams{ID: "cg-del"}, "CardGroupDelete", CardGroupDeleteParams{ID: "cg-del"}},
		{MethodSessionGroupsList, nil, "SessionGroupsList", nil},
		{MethodSessionGroupSave, SessionGroupSaveParams{Name: "sg-new"}, "SessionGroupSave", SessionGroupSaveParams{Name: "sg-new"}},
		{MethodSessionGroupSetMembers, SessionGroupSetMembersParams{ID: "sg-mem", Members: []string{"sess-a"}}, "SessionGroupSetMembers", SessionGroupSetMembersParams{ID: "sg-mem", Members: []string{"sess-a"}}},
		{MethodModelDefaultFor, DefaultForParams{Card: "card-dm"}, "ModelDefaultFor", DefaultForParams{Card: "card-dm"}},
		{MethodCardModelSet, CardModelSetParams{Card: "card-cm", Provider: "openai", Model: "gpt-5.6"}, "CardModelSet", CardModelSetParams{Card: "card-cm", Provider: "openai", Model: "gpt-5.6"}},
		{MethodSessionGroupDelete, SessionGroupDeleteParams{ID: "sg-del"}, "SessionGroupDelete", SessionGroupDeleteParams{ID: "sg-del"}},

		// --- directed authorship: the if-chain's trailing arm was PostLine ---
		{MethodPostLine, PostLineParams{Actor: "narrator", Text: "a line"}, "PostLine", PostLineParams{Actor: "narrator", Text: "a line"}},
		{MethodDirectTurn, DirectTurnParams{Text: "directed"}, "DirectTurn", DirectTurnParams{Text: "directed"}},
		{MethodTurnAdvance, nil, "AdvanceTurn", nil},

		// --- positional call sites: epoch/index/variant are three ints in a row,
		// and swapping any two is invisible to the compiler. ---
		{MethodVariantsPrune, VariantsPruneParams{Epoch: 3, Index: 5}, "PruneVariants", variantArgs{Epoch: 3, Index: 5}},
		{MethodVariantsDrop, VariantsDropParams{Epoch: 7, Index: 11, Variant: 2}, "DropVariant", variantArgs{Epoch: 7, Index: 11, Variant: 2}},
		// A third positional site: turn.continue unpacks p.Epoch out of its params.
		{MethodTurnContinue, TurnContinueParams{Epoch: 13}, "ContinueTurn", continueArgs{Epoch: 13}},

		// --- archive: three verbs behind one outer case, and restore is the one
		// that binds params. A default arm here would decode an archive frame as
		// a restore. ---
		{MethodSessionArchive, nil, "ArchiveSession", nil},
		{MethodSessionsArchived, nil, "ArchivedSessions", nil},
		{MethodSessionRestore, RestoreSessionParams{ID: "20260101-120000-aaaaaaaa"}, "RestoreSession", RestoreSessionParams{ID: "20260101-120000-aaaaaaaa"}},

		// --- workflows: two verbs behind one outer case, and get is the one that
		// binds params. Same trap as archive above. ---
		{MethodWorkflowsList, nil, "WorkflowRuns", nil},
		{MethodWorkflowsGet, WorkflowGetParams{ID: "wf_abc123"}, "WorkflowRun", WorkflowGetParams{ID: "wf_abc123"}},

		// --- draft (optional, takes no params — it must still reach the right arm) ---
		{MethodSessionDiscardDraft, nil, "DiscardDraft", nil},

		// --- export (optional; session-scoped, so the frame's sess must reach it) ---
		{MethodSessionsExport, SessionExportParams{Format: "markdown"}, "SessionsExport", SessionExportParams{Format: "markdown"}},

		// --- remaining optional controllers ---
		{MethodCastSpeak, CastSpeakParams{Actor: "speaker"}, "CastSpeak", CastSpeakParams{Actor: "speaker"}},
		{MethodNoteSet, NoteSetParams{Text: "a note"}, "NoteSet", NoteSetParams{Text: "a note"}},
		{MethodUserBind, UserBindParams{Name: "bound"}, "UserBind", UserBindParams{Name: "bound"}},
		{MethodSuggestReply, SuggestParams{}, "SuggestReply", nil},
		{MethodCardsDoctor, DoctorParams{ID: "checked"}, "CardsDoctor", DoctorParams{ID: "checked"}},
		{MethodSessionsDoctor, SessionDoctorParams{}, "SessionsDoctor", nil},
		{MethodSessionsNextScene, NextSceneParams{Title: "scene 2"}, "SessionsNextScene", nil},
		{MethodSessionsRealize, RealizeParams{}, "SessionsRealize", nil},
		{MethodAuthLoginStart, AuthLoginStartParams{Provider: "anthropic"}, "AuthLoginStart", AuthLoginStartParams{Provider: "anthropic"}},
		{MethodAuthLoginSubmit, AuthLoginSubmitParams{}, "AuthLoginSubmit", nil},
		{MethodAuthLoginCancel, AuthFlowRef{}, "AuthLoginCancel", nil},
		{MethodAuthLogout, AuthLogoutParams{Provider: "openai"}, "AuthLogout", AuthLogoutParams{Provider: "openai"}},
		{MethodAuthEndpointRemove, AuthEndpointRemoveParams{}, "AuthEndpointRemove", nil},
		{MethodReplayState, nil, "ReplayState", nil},
	}
}

// allGroups is every negotiable group, so group gating never masks a dispatch
// bug as an "unsupported" answer.
func allGroups() Contract {
	return Contract{
		Protocol: Protocol,
		Groups:   []Group{GroupConversation, GroupSession, GroupControl, GroupReplay, GroupAuth},
	}
}

func TestDispatchBindsEachVerbsOwnParams(t *testing.T) {
	for _, tc := range dispatchCases() {
		t.Run(string(tc.method), func(t *testing.T) {
			rec := &recorder{fakeSvc: &fakeSvc{}}
			var replies []Frame
			s := &serveState{
				svc:      rec,
				contract: allGroups(),
				write:    func(f Frame) error { replies = append(replies, f); return nil },
				subs:     map[string]context.CancelFunc{},
			}

			raw, err := json.Marshal(tc.params)
			if err != nil {
				t.Fatalf("marshal params: %v", err)
			}
			if tc.params == nil {
				raw = nil
			}
			s.handle(context.Background(), Frame{
				Kind: KindCmd, ID: 1, Sess: dispatchSess, Method: tc.method, Params: raw,
			})

			// An unsupported/bad-request answer means the arm never ran; say so
			// loudly rather than failing on an empty `called`.
			for _, f := range replies {
				if f.Error != nil {
					t.Fatalf("%s answered %s: %s", tc.method, f.Error.Code, f.Error.Message)
				}
			}
			if rec.called != tc.want {
				t.Fatalf("%s dispatched to %q, want %q", tc.method, rec.called, tc.want)
			}
			if tc.args != nil && !reflect.DeepEqual(rec.args, tc.args) {
				t.Fatalf("%s bound %#v, want %#v", tc.method, rec.args, tc.args)
			}
		})
	}
}

// A session-scoped verb must receive the frame's session. Passing "" (or another
// session) would apply a World rebind, a cast removal or a posted line to the
// wrong conversation — and every one of those is silent.
func TestDispatchForwardsTheFrameSession(t *testing.T) {
	sessionScoped := map[string]bool{
		"BackgroundBind": true, "CastAdd": true, "CastRemove": true, "CastSpeak": true,
		"PostLine": true, "DirectTurn": true, "AdvanceTurn": true, "NoteSet": true,
		"UserBind": true, "WorldLorePut": true, "WorldLoreDelete": true, "WorldSet": true,
		"WorldSave": true, "PruneVariants": true, "DropVariant": true, "SuggestReply": true,
		"SessionsDoctor": true, "SessionsNextScene": true, "ReplayState": true,
		"SessionsExport": true,
	}
	for _, tc := range dispatchCases() {
		if !sessionScoped[tc.want] {
			continue
		}
		t.Run(string(tc.method), func(t *testing.T) {
			rec := &recorder{fakeSvc: &fakeSvc{}}
			s := &serveState{
				svc: rec, contract: allGroups(),
				write: func(Frame) error { return nil },
				subs:  map[string]context.CancelFunc{},
			}
			raw, _ := json.Marshal(tc.params)
			if tc.params == nil {
				raw = nil
			}
			s.handle(context.Background(), Frame{
				Kind: KindCmd, ID: 1, Sess: dispatchSess, Method: tc.method, Params: raw,
			})
			if rec.sess != dispatchSess {
				t.Fatalf("%s forwarded session %q, want %q", tc.method, rec.sess, dispatchSess)
			}
		})
	}
}

// notCovered records the verbs this table deliberately does not drive, each with
// a reason — the shape forwarder_complete_test.go uses. Together with the table
// it must account for EVERY Method constant, so a new verb cannot quietly join
// neither set.
var notCovered = map[Method]string{
	// The mandatory WorkspaceService surface. Every one of these is dispatched
	// by a singleton `case` on the top-level switch — no group, no inner switch,
	// so the fallthrough bug this table exists for cannot occur there. Three are
	// additionally driven end-to-end by wire_test.go.
	MethodSubscribe:     "wire_test round trip",
	MethodPrompt:        "wire_test round trip",
	MethodApprove:       "wire_test asserts the bound decision",
	MethodReplayControl: "wire_test asserts the bound params",
	MethodAuthProviders: "wire_test drives it",

	MethodUnsubscribe: "mandatory, ungrouped", MethodQueue: "mandatory, ungrouped",
	MethodQueueSet: "mandatory, ungrouped", MethodCancel: "mandatory, ungrouped",
	MethodCompact: "mandatory, ungrouped", MethodClear: "mandatory, ungrouped",
	MethodAnswer: "mandatory, ungrouped", MethodMessageEdit: "mandatory, ungrouped",
	MethodMessageDelete: "mandatory, ungrouped", MethodTurnSwipe: "mandatory, ungrouped",
	MethodTurnRetry:    "mandatory, ungrouped",
	MethodSideChatOpen: "mandatory, ungrouped", MethodSideChatAsk: "mandatory, ungrouped",
	MethodSideChatClose: "mandatory, ungrouped",
	MethodSessionsList:  "mandatory, ungrouped", MethodSessionCreate: "mandatory, ungrouped",
	MethodSessionDelete: "mandatory, ungrouped", MethodSessionRename: "mandatory, ungrouped",
	MethodSessionGenerateTitle: "mandatory, ungrouped", MethodSessionFork: "mandatory, ungrouped",
	MethodSessionResume: "mandatory, ungrouped",
	MethodModelsList:    "mandatory, ungrouped", MethodModelSwitch: "mandatory, ungrouped",
	MethodModelFavorite: "mandatory, ungrouped", MethodModelSetDefault: "mandatory, ungrouped",
	MethodSurfaceGet: "mandatory, ungrouped", MethodSurfaceAction: "mandatory, ungrouped",
	MethodSurfacesList: "mandatory, ungrouped",
	MethodContextGet:   "mandatory, ungrouped", MethodContextNode: "mandatory, ungrouped",
	MethodFilesList: "mandatory, ungrouped", MethodI18nCatalog: "mandatory, ungrouped",
	MethodUsageSnapshot: "mandatory, ungrouped", MethodResetsList: "mandatory, ungrouped",
	MethodResetsConsume: "mandatory, ungrouped",
	MethodRestart:       "mandatory, ungrouped", MethodTrust: "mandatory, ungrouped",
	MethodUntrust:             "mandatory, ungrouped",
	MethodConversationHistory: "mandatory, ungrouped", MethodConversationReveal: "mandatory, ungrouped",

	// A genuine dead verb: a typed client method exists but nothing calls it;
	// the TUI gauge moved to usage.snapshot and orphaned it.
	MethodUsageGet: "no caller on any surface — see docs/reviews/2026-07-20",
}

// methodValues parses methods.go for `MethodX Method = "verb"`, returning
// constant name → wire value. methodConstants (methods_complete_test.go) reads
// only the names; the completeness check below needs the values too.
func methodValues(t *testing.T) map[string]Method {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parse methods.go: %v", err)
	}
	out := map[string]Method{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Method" {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[n.Name] = Method(v)
			}
		}
	}
	if len(out) < 10 {
		t.Fatalf("found only %d Method values; the parse is not seeing them", len(out))
	}
	return out
}

func TestDispatchTableAccountsForEveryMethod(t *testing.T) {
	covered := map[Method]bool{}
	for _, tc := range dispatchCases() {
		if covered[tc.method] {
			t.Fatalf("duplicate case for %s", tc.method)
		}
		covered[tc.method] = true
	}
	var missing []string
	for name, m := range methodValues(t) {
		if covered[m] || notCovered[m] != "" {
			continue
		}
		missing = append(missing, fmt.Sprintf("%s (%q)", name, m))
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("verbs in neither the dispatch table nor notCovered — add a case, or an "+
			"entry saying why it needs none:\n  %v", missing)
	}
}
