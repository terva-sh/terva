package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// Card groups on the wire. Like the card library itself, the Workspace is just
// the access point: the store is global ($TERVA_HOME/card-groups), so these are
// thin adapters over build.GroupStore. A group only files card refs into named
// buckets — it never touches a card — so, like cards.*, there is no trust gate.
//
// Membership lives in the group doc, so a card is never rewritten to be filed
// and deleting a card never has to reach into every group: a member whose card
// is gone is filtered out on read (liveCardIDs), which is why every path that
// returns a group runs it through cardGroupView.
var _ ctrlproto.CardGroupsController = (*Workspace)(nil)

func (w *Workspace) cardGroupStore() *build.GroupStore { return build.NewCardGroupStore() }

// liveCardIDs is the set of card refs that still resolve to a stored card — used
// to drop stale members from a group before it goes on the wire.
func (w *Workspace) liveCardIDs() map[string]bool {
	stored, err := w.cardStore().List()
	if err != nil {
		return nil
	}
	live := make(map[string]bool, len(stored))
	for _, sc := range stored {
		live[sc.ID] = true
	}
	return live
}

// cardGroupView projects a stored group into its wire form, keeping only members
// whose card still exists. A nil live-set (the card list failed to load) passes
// members through rather than blanking every group.
func cardGroupView(g build.Group, live map[string]bool) ctrlproto.GroupView {
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

// CardGroupsList returns every card group with its live members.
func (w *Workspace) CardGroupsList(_ context.Context) (ctrlproto.CardGroupsResult, error) {
	docs, err := w.cardGroupStore().List()
	if err != nil {
		return ctrlproto.CardGroupsResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "list card groups: %v", err)
	}
	live := w.liveCardIDs()
	out := make([]ctrlproto.GroupView, 0, len(docs))
	for _, g := range docs {
		out = append(out, cardGroupView(g, live))
	}
	return ctrlproto.CardGroupsResult{Groups: out}, nil
}

// CardGroupSave creates a group (empty id) or edits one's name/colour (existing
// id). Membership is untouched here — it rides cardgroups.set_members.
func (w *Workspace) CardGroupSave(_ context.Context, p ctrlproto.CardGroupSaveParams) (ctrlproto.GroupView, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "a group needs a name")
	}
	store := w.cardGroupStore()
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
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "save card group: %v", err)
	}
	w.broadcastLibraryChanged()
	return cardGroupView(saved, w.liveCardIDs()), nil
}

// CardGroupDelete removes a group; its member cards are untouched.
func (w *Workspace) CardGroupDelete(_ context.Context, p ctrlproto.CardGroupDeleteParams) error {
	if err := w.cardGroupStore().Delete(p.ID); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	w.broadcastLibraryChanged()
	return nil
}

// CardGroupSetMembers replaces a group's member list. Refs that don't resolve to
// a stored card are dropped, so a group never files a phantom id.
func (w *Workspace) CardGroupSetMembers(_ context.Context, p ctrlproto.CardGroupSetMembersParams) (ctrlproto.GroupView, error) {
	store := w.cardGroupStore()
	doc, err := store.Get(p.ID)
	if err != nil {
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	live := w.liveCardIDs()
	members := make([]string, 0, len(p.Members))
	for _, m := range p.Members {
		if m = strings.TrimSpace(m); m != "" && (live == nil || live[m]) {
			members = append(members, m)
		}
	}
	doc.Members = members
	saved, err := store.Save(doc)
	if err != nil {
		return ctrlproto.GroupView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "set card group members: %v", err)
	}
	w.broadcastLibraryChanged()
	return cardGroupView(saved, live), nil
}
