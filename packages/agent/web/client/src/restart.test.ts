import { describe, expect, it } from 'vitest'

import { ClientCodes, WireCodes, WireError } from './platform/ctrlproto/errors'
import { restartRejection } from './restart'

// These cases used to construct plain `new Error('connection closed')` and
// assert on the concatenated `code: message` text, because that is what the
// client rejected with and what restartRejection matched. Both changed on
// purpose: the rejection now carries the code as a field, and the text a human
// sees no longer has the machine code welded to the front of it.
describe('restartRejection', () => {
  it('swallows the expected socket-drop that races the ack', () => {
    // A successful restart resolves; only a socket drop racing the ack rejects
    // with this, and the client auto-reconnects — nothing to show.
    expect(restartRejection(new WireError(ClientCodes.connectionClosed, 'connection closed'))).toBeNull()
  })

  it('surfaces a structured control-plane refusal (unsupported platform)', () => {
    // Windows: relaunch.Trigger returns unsupported; the button must not look dead.
    expect(
      restartRejection(new WireError(WireCodes.unsupported, 'self-restart is not supported on this platform')),
    ).toBe('self-restart is not supported on this platform')
  })

  it('surfaces a failed preflight / go-run refusal', () => {
    expect(
      restartRejection(new WireError(WireCodes.internal, 'restart: cannot self-restart a `go run` build')),
    ).toBe('restart: cannot self-restart a `go run` build')
  })

  it('surfaces a not-connected rejection as a sentence, not a code', () => {
    const msg = restartRejection(new WireError(ClientCodes.notConnected, 'not connected'))
    expect(msg).not.toBeNull()
    expect(msg).not.toMatch(/^Error:/)
    expect(msg).not.toContain('client.')
  })

  it('stringifies a non-Error rejection', () => {
    expect(restartRejection('boom')).toBe('boom')
  })

  // A plain Error is no longer the close race, however it is worded. Matching
  // prose is what coupled this function to a string literal in client.ts.
  it('does not swallow a plain Error that merely says connection closed', () => {
    expect(restartRejection(new Error('connection closed'))).toBe('connection closed')
  })
})
