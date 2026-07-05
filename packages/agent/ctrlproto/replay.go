package ctrlproto

import "context"

// ReplayState is a replay session's transport snapshot — the result of
// replay.control / replay.state and the payload of an [EventReplayState]
// broadcast, so every connected scrubber converges.
type ReplayState struct {
	Playing  bool    `json:"playing"`
	Position int     `json:"position"`       // index of the next frame to emit
	Total    int     `json:"total"`          // frames in the scene
	Speed    float64 `json:"speed"`          // delay multiplier (2 = twice as fast)
	Mode     string  `json:"mode,omitempty"` // effective | raw
}

// ReplayControlParams drives a replay's transport (replay.control), action-
// dispatched like [SurfaceActionParams]. Only the field the action needs is read.
type ReplayControlParams struct {
	Action     string  `json:"action"`               // play | pause | step | seek | turn | speed
	Position   int     `json:"position,omitempty"`   // seek: target frame index; turn: signed turn delta (+1/-1)
	Multiplier float64 `json:"multiplier,omitempty"` // speed: delay multiplier
	Unit       string  `json:"unit,omitempty"`       // step: granularity (event; message/turn later)
}

// ReplayStateResult is the response to replay.control and replay.state.
type ReplayStateResult struct {
	State ReplayState `json:"state"`
}

// ReplayController is the optional capability a [WorkspaceService] implements to
// serve the replay method group. [ServeConn] type-asserts it: a service that
// does not implement it rejects replay.* with [CodeUnsupported]. It is kept off
// the base WorkspaceService deliberately — the live workspace hosts live turns,
// not a file playhead, so it should not have to stub transport it cannot do; a
// replay-backed carrier (or, later, a workspace that also offers replay) opts in
// by implementing these two methods, and advertises [GroupReplay] in its hello.
type ReplayController interface {
	// ReplayControl applies a transport action to sess and returns the new state.
	ReplayControl(ctx context.Context, sess string, p ReplayControlParams) (ReplayState, error)
	// ReplayState returns sess's current transport state.
	ReplayState(ctx context.Context, sess string) (ReplayState, error)
}
