package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// Session groups on the wire — the session-id twin of card groups
// (workspace_cardgroups.go), over the $TERVA_HOME/session-groups store. A group
// only files session ids into named buckets; it never touches a session, so like
// sessions.* there is no trust gate. Membership lives in the group doc, so a stale
// member (a session since deleted) is filtered out on read (liveSessionIDs).
//
// A mutation broadcasts SessionsChangedEvent so an open sessions board or Stage
// library re-lists and re-fetches its groups — the same event a rename fires.
var _ ctrlproto.SessionGroupsController = (*Workspace)(nil)

func (w *Workspace) sessionGroupStore() *build.GroupStore { return build.NewSessionGroupStore() }

// liveSessionIDs is the set of session ids that still resolve — used to drop
// stale members from a group before it goes on the wire.
func (w *Workspace) liveSessionIDs(ctx context.Context) map[string]bool {
	sessions, err := w.Sessions(ctx)
	if err != nil {
		return nil
	}
	live := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		live[s.ID] = true
	}
	return live
}

// sessionGroupView projects a stored group into its wire form, keeping only
// members whose session still exists. A nil live-set passes members through
// rather than blanking every group.
func sessionGroupView(g build.Group, live map[string]bool) ctrlproto.GroupView {
	members := g.Members
	if live != nil {
		members = make([]string, 0, len(g.Members))
		for _, m := range g.Members {
			if live[m] {
				members = append(members, m)
			}
		}
	}
	return ctrlproto.GroupView{ID: g.ID, Name: g.Name, Color: g.Color, Members: members}
}

// SessionGroupsList returns every session group with its live members.
func (w *Workspace) SessionGroupsList(ctx context.Context) (ctrlproto.SessionGroupsResult, error) {
	docs, err := w.sessionGroupStore().List()
	if err != nil {
		return ctrlproto.SessionGroupsResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "list session groups: %v", err)
	}
	live := w.liveSessionIDs(ctx)
	out := make([]ctrlproto.GroupView, 0, len(docs))
	for _, g := range docs {
		out = append(out, sessionGroupView(g, live))
	}
	return ctrlproto.SessionGroupsResult{Groups: out}, nil
}

// SessionGroupSave creates a group (empty id) or edits one's name/colour
// (existing id). Membership is untouched here — it rides sessiongroups.set_members.
func (w *Workspace) SessionGroupSave(ctx context.Context, p ctrlproto.SessionGroupSaveParams) (ctrlproto.GroupView, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "a group needs a name")
	}
	store := w.sessionGroupStore()
	doc := build.Group{ID: p.ID, Name: name, Color: strings.TrimSpace(p.Color)}
	if p.ID != "" {
		prev, err := store.Get(p.ID)
		if err != nil {
			return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
		}
		doc.Members = prev.Members // save is metadata-only; keep the membership
	}
	saved, err := store.Save(doc)
	if err != nil {
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "save session group: %v", err)
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return sessionGroupView(saved, w.liveSessionIDs(ctx)), nil
}

// SessionGroupDelete removes a group; its member sessions are untouched.
func (w *Workspace) SessionGroupDelete(_ context.Context, p ctrlproto.SessionGroupDeleteParams) error {
	if err := w.sessionGroupStore().Delete(p.ID); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return nil
}

// SessionGroupSetMembers replaces a group's member list. Ids that don't resolve
// to a session are dropped, so a group never files a phantom id.
func (w *Workspace) SessionGroupSetMembers(ctx context.Context, p ctrlproto.SessionGroupSetMembersParams) (ctrlproto.GroupView, error) {
	store := w.sessionGroupStore()
	doc, err := store.Get(p.ID)
	if err != nil {
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	live := w.liveSessionIDs(ctx)
	members := make([]string, 0, len(p.Members))
	for _, m := range p.Members {
		if m = strings.TrimSpace(m); m != "" && (live == nil || live[m]) {
			members = append(members, m)
		}
	}
	doc.Members = members
	saved, err := store.Save(doc)
	if err != nil {
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "set session group members: %v", err)
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return sessionGroupView(saved, live), nil
}
