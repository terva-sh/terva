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

// DefaultBackoff is the pause between a member's dial attempts. It matches
// ctrlclient.DefaultBackoff, because the two loops are the same loop seen from
// opposite ends and a mismatch would only be confusing.
const DefaultBackoff = 1500 * time.Millisecond
