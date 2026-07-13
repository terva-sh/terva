import { describe, expect, it } from 'vitest'

import { restartRejection } from './restart'

describe('restartRejection', () => {
  it('swallows the expected socket-drop that races the ack', () => {
    // A successful restart resolves; only a socket drop racing the ack rejects
    // with this, and the client auto-reconnects — nothing to show.
    expect(restartRejection(new Error('connection closed'))).toBeNull()
  })

  it('surfaces a structured control-plane refusal (unsupported platform)', () => {
    // Windows: relaunch.Trigger returns unsupported; the button must not look dead.
    expect(restartRejection(new Error('unsupported: self-restart is not supported on this platform'))).toBe(
      'unsupported: self-restart is not supported on this platform',
    )
  })

  it('surfaces a failed preflight / go-run refusal', () => {
    expect(restartRejection(new Error('internal: restart: cannot self-restart a `go run` build'))).toBe(
      'internal: restart: cannot self-restart a `go run` build',
    )
  })

  it('surfaces a not-connected rejection', () => {
    expect(restartRejection(new Error('not connected'))).toBe('not connected')
  })

  it('stringifies a non-Error rejection', () => {
    expect(restartRejection('boom')).toBe('boom')
  })
})
