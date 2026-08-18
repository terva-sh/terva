package ctrlproto

import "context"

// The play cast on the wire. A play session's cast — actor name → persona/card
// ref — is set at creation (CreateOpts.Cast) and surfaced read-only on
// SessionInfo.Cast; these verbs make it editable mid-scene, so the director's
// ensemble can grow or shrink as a story does. Each change rebuilds the
// actor_spawn tool (its `actor` enum) and the cast addendum, a deliberate
// cached-prefix rebuild like a user-persona name change.
//
// CastController is OPTIONAL, like the other Stage controllers (NoteController,
// UserController): a carrier without a live workspace (a replay session, a test
// fake) simply does not implement it and the verb answers "unsupported" rather
// than rippling to every WorkspaceService implementer.
//
// A roster is an IMMERSIVE concept, not a --play one: both verbs serve a chat
// session too, where the roster is the directed-authorship cast — voiced on
// demand, with no warm agents and no actor_spawn tool. Only a coding session
// (Experience == "") is a bad request. This said "Cast is a --play concept, so
// both verbs are a bad request on a chat or coding session" while
// workspace_cast.go had long since been written the other way, down to its
// error string: "a roster is only available in a chat or play session".
type CastController interface {
	// CastAdd adds or updates one cast member (actor name → ref). The ref is a
	// persona name or a character-card path, validated before it persists; an
	// unresolvable ref is a bad request.
	CastAdd(ctx context.Context, sess string, p CastMemberParams) error
	// CastRemove drops a cast member by name and retires its warm actor, if any.
	CastRemove(ctx context.Context, sess string, p CastMemberParams) error
	// CastSpeak directs the narrator to bring one cast member into the scene now —
	// the user-directs move ("pick who speaks"). The narrator stays the source of
	// truth and voices the actor (via actor_spawn), so this runs a normal turn and
	// returns once it is accepted, like Prompt; [CodeBusy] if a turn is running.
	CastSpeak(ctx context.Context, sess string, p CastSpeakParams) error
}

// CastMemberParams names a cast member. Ref (the persona/card the actor speaks
// as) is required for cast.add and ignored by cast.remove.
type CastMemberParams struct {
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
	// Provider/Model optionally pin this actor to a specific model (Phase 7); empty
	// inherits the session/host route. cast.add is an upsert, so re-adding a member
	// with a different pin changes it.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// CastRoute is one cast member's pinned provider+model (Phase 7); empty fields
// mean the actor inherits the session/host route.
type CastRoute struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// CastSpeakParams names the cast member to bring into the scene.
type CastSpeakParams struct {
	Actor string `json:"actor"`
}
