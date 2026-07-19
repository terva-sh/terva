package workspace

import (
	"context"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
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
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "cast.add needs a name and a ref")
	}
	s, err := w.castSession(sess)
	if err != nil {
		return err
	}
	next := s.castRefs()
	next[name] = ref
	return s.applyCast(next, nil)
}

// CastRemove drops a cast member and retires its warm actor.
func (w *Workspace) CastRemove(_ context.Context, sess string, p ctrlproto.CastMemberParams) error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "cast.remove needs a name")
	}
	s, err := w.castSession(sess)
	if err != nil {
		return err
	}
	next := s.castRefs()
	if _, ok := next[name]; !ok {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "no cast member %q", name)
	}
	delete(next, name)
	return s.applyCast(next, []string{name})
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
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "cast.speak needs an actor")
	}
	if s.sess.Meta.Experience != build.ExperiencePlay {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "a cast is only available for play sessions")
	}
	if _, ok := s.castRefs()[actor]; !ok {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "no cast member %q", actor)
	}
	directive := fmt.Sprintf("[Direction] Bring %s into the scene now — let them speak to this moment.", actor)
	return s.promptBlocks(directive, nil)
}

// castSession resolves sess and gates cast edits: a cast is a --play concept, and
// tearing down a live actor / swapping the actor_spawn tool mid-turn is unsafe,
// so an idle play session is required.
func (w *Workspace) castSession(sess string) (*wsSession, error) {
	s, err := w.resolve(sess)
	if err != nil {
		return nil, err
	}
	if s.sess.Meta.Experience != build.ExperiencePlay {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "a cast is only available for play sessions")
	}
	if s.busy() {
		return nil, ctrlproto.Errorf(ctrlproto.CodeBusy, "cannot change the cast while a turn is running")
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

// applyCast validates the new cast, persists it, rebuilds the actor_spawn tool +
// cast addendum, and retires any removed actors' warm agents. The refs in
// `removed` have already been dropped from `next`.
func (s *wsSession) applyCast(next map[string]string, removed []string) error {
	// Validate against the merged (project + session) cast so a bad ref is
	// rejected before anything persists or tears down.
	args := s.argsSnapshot()
	args.Cast = next
	built, err := build.BuildActorCast(build.MergedCastRefs(args, args.CWD, s.trusted.Load()), args.CWD)
	if err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%v", err)
	}
	if err := s.sess.SetCast(next); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "set cast: %v", err)
	}
	s.mu.Lock()
	s.args.Cast = next
	s.actorCast = built
	if len(built) > 0 && s.warmActors == nil {
		s.warmActors = tools.NewWarmActors(tools.DefaultWarmActorCap)
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
	// The cast reshapes the cached prefix (actor enum + addendum), so rebuild the
	// prompt/tools — it pins for the next turn and emits the prompt-rebuilt notice.
	s.rebuildTools("cast")
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}
