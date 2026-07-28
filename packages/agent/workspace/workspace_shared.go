package workspace

import (
	"context"
	"time"

	"terva.sh/terva/packages/core"
)

// sharePublisher binds the workspace's share store to one session, which is the
// whole of what tools.ShareFileTool needs from the host. The session id is bound
// at registration rather than passed per call, so a tool has no way to publish
// into a conversation it is not part of.
type sharePublisher struct {
	w    *Workspace
	sess string
}

// Publish copies the file into this session's share area and describes it.
//
// The description is rebuilt from what actually landed on disk (attach.Ref does
// that reconstruction), so the name, type, and size the panel renders are the
// store's facts and not the model's claims — the same rule the inbound direction
// applies to a client's declared content type. CallID is left empty; the agent
// loop stamps it, because a tool cannot know its own call.
func (p sharePublisher) Publish(_ context.Context, path, name string) (core.SharedFile, error) {
	ref, err := p.w.shared.Publish(p.sess, path, name)
	if err != nil {
		return core.SharedFile{}, err
	}
	out := core.SharedFile{
		ID:   ref.ID,
		Name: ref.Name,
		Kind: ref.Kind,
		Mime: ref.Mime,
		Size: ref.Size,
	}
	// Stamped once, here, and then persisted with the turn — not recomputed on
	// read. The store's own answer at publish time is the honest one: recomputing
	// later would need the file, and the whole point of the field is to say
	// something useful once the file is gone.
	if !ref.ExpiresAt.IsZero() {
		out.ExpiresAt = ref.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out, nil
}
