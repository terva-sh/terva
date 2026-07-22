package ctrlproto

import "context"

// Session groups on the wire — the exact shape of the card-group namespace
// (CardGroupsController) over session ids instead of card refs. A session group
// is a terva-owned membership bucket for browsing chats and plays, distinct from
// a World (which seeds and groups sessions but carries roster/lore/coordination):
// a group is only a name and a member list. These verbs move session ids in and
// out of buckets and never touch a session, so they are ungated like sessions.*.
//
// SessionGroupsController is OPTIONAL, like CardGroupsController; both reuse the
// GroupView wire type. Unlike card groups, session groups appear on BOTH the
// Stage library and the control panel — but the verbs are the same either way.
type SessionGroupsController interface {
	// SessionGroupsList returns every session group, each with its live members
	// (session ids whose session still exists — stale ids are filtered out).
	SessionGroupsList(ctx context.Context) (SessionGroupsResult, error)
	// SessionGroupSave creates a group (empty id) or updates one's name/colour
	// (existing id), leaving its members untouched.
	SessionGroupSave(ctx context.Context, p SessionGroupSaveParams) (GroupView, error)
	// SessionGroupDelete removes a group; its member sessions are untouched.
	SessionGroupDelete(ctx context.Context, p SessionGroupDeleteParams) error
	// SessionGroupSetMembers replaces a group's member list (session ids).
	// Unknown ids are dropped. The sole membership mutation.
	SessionGroupSetMembers(ctx context.Context, p SessionGroupSetMembersParams) (GroupView, error)
}

// SessionGroupsResult is the payload of sessiongroups.list.
type SessionGroupsResult struct {
	Groups []GroupView `json:"groups"`
}

// SessionGroupSaveParams creates (empty ID) or renames/recolours (existing ID) a
// session group; members ride sessiongroups.set_members.
type SessionGroupSaveParams struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// SessionGroupDeleteParams names the group to remove.
type SessionGroupDeleteParams struct {
	ID string `json:"id"`
}

// SessionGroupSetMembersParams replaces a group's members with the given session
// ids.
type SessionGroupSetMembersParams struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
}
