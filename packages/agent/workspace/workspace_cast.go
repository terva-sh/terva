package workspace

import (
	"context"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The play cast on the wire. cast.add / cast.remove edit a play session's
// ensemble mid-scene: they write SessionMeta.Cast (a durable last-wins meta row)
// and rebuild the actor_spawn tool + cast addendum so the director can voice the
// new roster on the next turn. Changing the cast reshapes the cached prefix (the
// actor `enum` and the addendum), so it goes through rebuildTools exactly like a
// user-persona name change. Optional controller — no WorkspaceService ripple.
var _ ctrlproto.CastController = (*Workspace)(nil)

// CastAdd adds or updates one cast member. The ref (a persona name or a card
// path) is validated before anything persists, so a typo fails loudly here
// rather than opaquely mid-scene.
func (w *Workspace) CastAdd(_ context.Context, sess string, p ctrlproto.CastMemberParams) error {
	name := strings.TrimSpace(p.Name)
	ref := strings.TrimSpace(p.Ref)
	if name == "" || ref == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("cast.add needs a name and a ref"))
	}
	s, err := w.castSession(sess)
	if err != nil {
		return err
	}
	// In chat the bound character is already on stage — re-adding them would
	// turn the meta-narrator on for a one-character scene (and list them twice
	// in the router prompt). routableRoster filters reads for sessions that
	// already carry such an entry; this keeps new ones from being written.
	if s.sess.Meta.Experience == "chat" {
		boundName, _ := s.boundCharacter()
		if ref == strings.TrimSpace(s.sess.Meta.Card) || strings.EqualFold(name, boundName) {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("%s is already on stage as the main character", name))
		}
	}
	next := s.castRefs()
	next[name] = ref
	models := s.castModels()
	if pinModel := strings.TrimSpace(p.Model); pinModel != "" {
		models[name] = core.CastRoute{Provider: strings.TrimSpace(p.Provider), Model: pinModel}
	} else {
		delete(models, name) // re-adding without a model clears any prior pin
	}
	return s.applyCast(next, models, nil)
}

// CastRemove drops a cast member and retires its warm actor.
func (w *Workspace) CastRemove(_ context.Context, sess string, p ctrlproto.CastMemberParams) error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("cast.remove needs a name"))
	}
	s, err := w.castSession(sess)
	if err != nil {
		return err
	}
	next := s.castRefs()
	if _, ok := next[name]; !ok {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no cast member %q", name))
	}
	delete(next, name)
	models := s.castModels()
	delete(models, name)
	return s.applyCast(next, models, []string{name})
}

// CastSpeak is the user-directs move: the client picks who speaks, and the
// narrator (Kertoja) is directed to bring that actor into the scene now. It runs
// a normal turn — the narrator stays the source of truth and voices the actor
// (via actor_spawn), so this reuses the whole turn + attribution path rather
// than injecting a line out of band. Play sessions with the actor in the cast
// only; [CodeBusy] when a turn is already running.
func (w *Workspace) CastSpeak(_ context.Context, sess string, p ctrlproto.CastSpeakParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	return s.speak(strings.TrimSpace(p.Actor))
}

// speak validates the actor and starts a directed turn (the CastSpeak body,
// split out so the turn path is testable with a gated client).
func (s *wsSession) speak(actor string) error {
	if actor == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("cast.speak needs an actor"))
	}
	if s.sess.Meta.Experience != build.ExperiencePlay {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a cast is only available for play sessions"))
	}
	if _, ok := s.castRefs()[actor]; !ok {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no cast member %q", actor))
	}
	directive := directionDirective(fmt.Sprintf("Bring %s into the scene now — let them speak to this moment.", actor))
	return s.promptBlocks(directive, nil, core.UserMessageExtras{})
}

// castSession resolves sess and gates cast edits: any IDLE immersive session may
// hold a roster — chat or play — because tearing down a live actor or swapping
// the actor_spawn tool mid-turn is unsafe, and a coding session has no roster at
// all.
//
// The doc here used to say "a cast is a --play concept ... so an idle play
// session is required", three lines above the check that admits chat.
func (w *Workspace) castSession(sess string) (*wsSession, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return nil, err
	}
	// The roster is a World concept (Worlds W2): available in any immersive
	// session. In a play session it warms actors for actor_spawn; in a chat it is
	// the directed-authorship roster, voiced on demand (post.line/suggest) with no
	// warm agents. applyCast gates the play-only machinery.
	if s.sess == nil || s.sess.Meta.Experience == "" {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a roster is only available in a chat or play session"))
	}
	if s.busy() {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBusy, "%s", i18n.T("cannot change the roster while a turn is running"))
	}
	return s, nil
}

// castRefs returns a mutable copy of the session's declared cast refs.
func (s *wsSession) castRefs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.args.Cast))
	for k, v := range s.args.Cast {
		out[k] = v
	}
	return out
}

// castModels returns a mutable copy of the session's per-actor model pins
// (Phase 7), persisted in session meta parallel to the cast refs.
func (s *wsSession) castModels() map[string]core.CastRoute {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]core.CastRoute, len(s.sess.Meta.CastModels))
	for k, v := range s.sess.Meta.CastModels {
		out[k] = v
	}
	return out
}

// castRoutesToView converts the persisted per-actor model pins to their wire
// shape for SessionInfo; nil when there are none (so the field is omitted).
func castRoutesToView(models map[string]core.CastRoute) map[string]ctrlproto.CastRoute {
	if len(models) == 0 {
		return nil
	}
	out := make(map[string]ctrlproto.CastRoute, len(models))
	for name, r := range models {
		out[name] = ctrlproto.CastRoute{Provider: r.Provider, Model: r.Model}
	}
	return out
}

// applyCastModels overlays per-actor model pins onto a freshly-built cast: an
// actor named in `models` runs on its pinned provider+model; the rest inherit the
// host/tier route. A pin for a name not in the cast is simply ignored.
func applyCastModels(cast map[string]tools.CastMember, models map[string]core.CastRoute) {
	for name, route := range models {
		if m, ok := cast[name]; ok {
			m.Provider = route.Provider
			m.Model = route.Model
			cast[name] = m
		}
	}
}

// applyCast validates the new cast, persists it, rebuilds the actor_spawn tool +
// cast addendum, and retires any removed actors' warm agents. The refs in
// `removed` have already been dropped from `next`.
func (s *wsSession) applyCast(next map[string]string, models map[string]core.CastRoute, removed []string) error {
	args := s.argsSnapshot()
	args.Cast = next
	// Warm actors + the actor_spawn tool are a PLAY skin (CastSkinActive), built
	// here only in play — matching session materialize, and so a chat roster does
	// not warm agents or bust the prompt cache. Refs are validated either way: play
	// through BuildActorCast (personas / card paths), a chat roster against the card
	// store (library cards the directed-authorship picker offers).
	play := build.CastSkinActive(args)
	var built map[string]tools.CastMember
	if play {
		b, err := build.BuildActorCast(build.MergedCastRefs(args, args.CWD, s.trusted.Load()), args.CWD)
		if err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
		}
		applyCastModels(b, models) // overlay the Phase-7 per-actor model pins
		built = b
	} else {
		for name, ref := range next {
			if _, err := s.ws.cardStore().Get(ref); err != nil {
				return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown character card %q for %q", ref, name))
			}
		}
	}
	if err := s.sess.SetCast(next, models); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "set cast: %v", err)
	}
	s.mu.Lock()
	s.args.Cast = next
	if play {
		s.actorCast = built
		if len(built) > 0 && s.warmActors == nil {
			s.warmActors = tools.NewWarmActors(tools.DefaultWarmActorCap)
		}
	}
	warm := s.warmActors
	s.mu.Unlock()

	// Tear down the live agents of removed actors (a no-op for any that never
	// warmed up), mirroring the scene-teardown stop func.
	if warm != nil {
		for _, name := range removed {
			warm.Retire(name, func(id string) {
				_ = s.ws.swarm.Stop(id)
				_ = s.ws.swarm.Remove(id)
			})
		}
	}
	// In play the cast reshapes the cached prefix (actor enum + addendum), so
	// rebuild the prompt/tools. A chat roster touches neither, so skip the rebuild
	// (no spurious prompt-rebuilt notice, no uncached next turn) and just broadcast
	// the new roster so the directed-authorship surface sees it.
	if play {
		s.rebuildTools("cast")
	}
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}
