package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

// The user persona on the wire. user.bind writes SessionMeta (UserName +
// UserDescription, a durable last-wins meta row) and applies both halves live:
// the DESCRIPTION updates the build.NoteRecord the per-turn tail reads (free,
// next turn), while a changed NAME threads into build Args.As and rebuilds the
// cached prefix — a deliberate cache bust, since the {{user}} macro is baked into
// the charter / greeting / lore. Optional controller, so the verb does not
// ripple to the other WorkspaceService implementers.
var _ ctrlproto.UserController = (*Workspace)(nil)

// UserBind binds (or, with empty fields, clears) a session's user persona. Only
// immersive sessions carry a user-persona record; a coding session is a bad
// request. A Ref names a SAVED user persona (userpersonas.*) to apply — its name
// and description win over the inline fields — so a client can bind a stored
// identity by reference or set an ad-hoc one inline.
func (w *Workspace) UserBind(_ context.Context, sess string, p ctrlproto.UserBindParams) error {
	name := strings.TrimSpace(p.Name)
	desc := strings.TrimSpace(p.Description)
	gender := strings.TrimSpace(p.Gender)
	pronouns := strings.TrimSpace(p.Pronouns)
	if ref := strings.TrimSpace(p.Ref); ref != "" {
		up, err := w.userPersonaStore().Get(ref)
		if err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no saved user persona %q", ref))
		}
		name, desc, gender, pronouns = up.Name, up.Description, up.Gender, up.Pronouns
	}
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if s.user == nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("the user persona is only available for chat/play sessions"))
	}
	if err := s.sess.SetUserPersona(name, desc, gender, pronouns); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "set user persona: %v", err)
	}
	// The description takes effect next turn for free — update the live tail record.
	s.user.Set(desc)
	// A changed name is the {{user}} macro in the cached prefix, so it needs a
	// deliberate rebuild: thread the new name into args, rebuild the prompt prefix
	// (rebuildTools re-Resolves + SetSystem, emitting the honest prompt-rebuilt
	// cache-bust notice), then re-wire the tail (reloadLore) so the triggered lore
	// and the user-persona frame pick up the new {{user}} name too. Gender/pronouns
	// ride the uncached tail (Args → the user-persona frame), so a change there is a
	// cheap tail rewire (reloadLore) with no cache bust. An unchanged name AND
	// unchanged gender/pronouns skips it all — a description-only edit stays free.
	s.mu.Lock()
	nameChanged := s.args.As != name
	tailIdentityChanged := s.args.UserGender != gender || s.args.UserPronouns != pronouns
	s.args.As = name
	s.args.UserGender = gender
	s.args.UserPronouns = pronouns
	s.mu.Unlock()
	if nameChanged {
		s.rebuildTools("user-persona")
		s.reloadLore()
		// If the greeting is still deferred (no real turn yet), re-derive it with the
		// new {{user}} and re-seed it in memory — so a fresh chat's opening reflects
		// the just-set persona instead of staying frozen with the old name (rough-edge
		// #6). After the first turn the greeting is on disk and only the prefix
		// rebuilds (above); the already-shown opening keeps its text, as before.
		if s.sess.HasPendingGreeting() {
			if rr, err := build.Resolve(s.argsSnapshot(), true); err == nil && len(rr.CardGreetings) > 0 {
				s.seedDeferredGreeting(s.sess, rr.CardGreetings)
			}
		}
	} else if tailIdentityChanged {
		// Gender/pronouns only — re-wire the tail so the user-persona frame picks up
		// the new values next turn, without the prefix rebuild a name change forces.
		s.reloadLore()
	}
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}
