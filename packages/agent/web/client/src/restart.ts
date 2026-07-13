// restartRejection decides how to treat a rejected control.restart request.
//
// A SUCCESSFUL restart resolves: the daemon acks control.restart, then replaces
// its image a beat later (relaunch.Delay), so the ack reaches the client before
// the socket drops. A rejection is therefore a real failure the operator should
// see — an unsupported platform, a `go run` build, a failed preflight, or a
// disabled capability (all structured `code: message` errors), or "not
// connected" when the socket wasn't open. The one expected rejection is the rare
// case where the socket drop races the ack, surfaced as "connection closed".
//
// Returns the error text to surface as a toast, or null to swallow.
export function restartRejection(e: unknown): string | null {
  const msg = e instanceof Error ? e.message : String(e)
  if (msg === 'connection closed') return null
  return msg
}
