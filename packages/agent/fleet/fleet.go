// Package fleet carries one terva daemon's control plane to a hub that fronts
// several of them.
//
// The member dials the hub. The hub never dials the member. Decision 0014
// records why: dialling out needs every member sitting at a routable address
// with an open port, which is exactly what a laptop behind NAT does not have.
// Reachability runs one way, so the connection does too.
//
// None of this changes the wire. ctrlproto.ServeConn takes a FrameConn rather
// than a listener, and ctrlclient.Options.Dial returns that same FrameConn, so
// neither role is tied to who opened the socket. A member hands the websocket
// it dialled to ServeConn and serves. The hub hands ctrlclient a Dial that
// returns the websocket it accepted.
//
// The roles are therefore crossed relative to the TCP open, and the hello
// ordering follows ctrlproto rather than the transport. ctrlclient writes its
// hello first and ServeConn reads first, so the hub speaks first at the
// protocol level even though the member opened the socket. Getting that
// backwards hangs instead of failing, because both ends would sit reading.
//
// This package carries no build tag on purpose. A lean binary built without
// terva_web can still be a fleet member. Only the hub's browser half needs the
// tag, and gorilla is already an untagged dependency through ctrlclient.
package fleet

import "time"

// writeTimeout bounds one frame write. A member that has gone away without
// closing otherwise parks a hub write forever, holding the frame mutex and
// stalling every other writer on that connection.
const writeTimeout = 15 * time.Second

// A fleet connection is silent whenever nobody is watching a member, which is
// most of the time, and a middlebox reads silence as death. The hub is
// reachable from another machine only through a tunnel while member identity is
// a shared bearer, so every member connection has at least one hop that can
// time out without telling either end.
//
// These match web/conn.go rather than differing for no reason. That file chose
// them against a real incident: a 50 second haproxy timeout cutting idle
// panels on the dot, with no bug anywhere in terva that a test could see.
const (
	// pingInterval is how often the hub pings an otherwise idle member.
	// Comfortably under any proxy idle timeout worth calling a default.
	pingInterval = 20 * time.Second
	// pongWait is how long a member may go without ANY traffic, a pong or a
	// frame or anything else, before the hub calls it dead. Three missed pings.
	//
	// This is also the fleet's worst-case stall. ctrlclient.Call has no timeout
	// of its own and waits on the caller's context, but teardownConn hands every
	// pending call an ErrDisconnected the moment a connection is torn down. So
	// the read deadline is what bounds a hung call, and this constant is the
	// bound.
	pongWait = 65 * time.Second
)

// DefaultBackoff is the pause between a member's dial attempts. It matches
// ctrlclient.DefaultBackoff, because the two loops are the same loop seen from
// opposite ends and a mismatch would only be confusing.
const DefaultBackoff = 1500 * time.Millisecond
