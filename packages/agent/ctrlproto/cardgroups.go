package ctrlproto

import "context"

// Card groups on the wire. A group is a terva-owned membership bucket the
// library browses by — see build.Group for how it differs from a card's CCv2
// tags (author labels inside the card) and from a World (a session-seeding
// object). These verbs only move ids in and out of buckets; they never touch a
// card, so like CardsController they are ungated beyond the workspace's own auth.
//
// CardGroupsController is OPTIONAL, like CardsController: a carrier with no
// library simply does not implement it and the verb answers "unsupported". The
// session-group namespace (SessionGroupsController) is the exact same shape over
// session ids, and reuses the Group wire type below.
type CardGroupsController interface {
	// CardGroupsList returns every card group, each with its live members (card
	// refs whose card still exists — stale ids are filtered out).
	CardGroupsList(ctx context.Context) (CardGroupsResult, error)
	// CardGroupSave creates a group (empty id) or updates one's name/colour
	// (existing id), leaving its members untouched.
	CardGroupSave(ctx context.Context, p CardGroupSaveParams) (GroupView, error)
	// CardGroupDelete removes a group. Its member cards are untouched — a group
	// is a view, not a container.
	CardGroupDelete(ctx context.Context, p CardGroupDeleteParams) error
	// CardGroupSetMembers replaces a group's member list (card refs). Unknown
	// refs are dropped. This is the sole membership mutation: adding or removing
	// one card is done by sending the group's new full list.
	CardGroupSetMembers(ctx context.Context, p CardGroupSetMembersParams) (GroupView, error)
}

// GroupView is the wire view of a membership bucket (build.Group), shared by the
// card-group and session-group namespaces — identical shape, the members being
// card refs in one and session ids in the other. Members are always the LIVE
// set (ids whose card/session still exists), so a count is just Members length.
// (Named GroupView, not Group, because Group is already this package's
// method-group enum.)
type GroupView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Color   string   `json:"color,omitempty"`
	Members []string `json:"members"`
}

// CardGroupsResult is the payload of cardgroups.list.
type CardGroupsResult struct {
	Groups []GroupView `json:"groups"`
}

// CardGroupSaveParams creates (empty ID) or updates (existing ID) a card group.
// Only the name and colour are set here; members ride cardgroups.set_members.
type CardGroupSaveParams struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// CardGroupDeleteParams names the group to remove.
type CardGroupDeleteParams struct {
	ID string `json:"id"`
}

// CardGroupSetMembersParams replaces a group's members with the given card refs.
type CardGroupSetMembersParams struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
}
