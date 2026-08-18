import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { ClientCodes, WireCodes, WireError, errText, isWireCode } from './errors'
import { restartRejection } from '../../restart'

// The 100 hand-written `String(e)` handlers rendered a busy session as
//
//   Error: busy: a turn is already running
//
// — JavaScript's own "Error: " prefix on top of the machine code the client
// baked into the message, over a sentence the daemon had marked with i18n.M so
// it could be translated. These assertions are about that output, not about
// the plumbing that produces it.

describe('errText', () => {
  it('renders a wire error as the sentence alone', () => {
    const e = new WireError(WireCodes.busy, 'a turn is already running')
    expect(errText(e)).toBe('a turn is already running')
  })

  it('never leaks the machine code or the Error prefix', () => {
    for (const e of [
      new WireError(WireCodes.busy, 'a turn is already running'),
      new WireError(WireCodes.notFound, 'no such card'),
      new WireError(WireCodes.badRequest, 'malformed params'),
      new Error('plain failure'),
      'a bare string',
      { weird: true },
      null,
      undefined,
    ]) {
      const text = errText(e)
      expect(text, `errText(${String(e)}) leaked the Error prefix`).not.toMatch(/^Error:/)
      expect(text, 'errText returned an empty string, which renders as a blank toast').not.toBe('')
      expect(text, 'errText stringified an object').not.toContain('[object')
    }
  })

  it('does not put the code in front of the message', () => {
    // The specific regression: `${code}: ${message}`.
    expect(errText(new WireError(WireCodes.busy, 'a turn is already running'))).not.toContain('busy:')
  })

  it('gives a terse code a sentence of its own', () => {
    // "unauthorized" alone is not something to show a person.
    expect(errText(new WireError(WireCodes.unauthorized, 'unauthorized'))).not.toBe('unauthorized')
    expect(errText(new WireError(ClientCodes.connectionClosed, 'connection closed'))).not.toBe('connection closed')
  })

  it('falls back to the daemon sentence for codes it does not know', () => {
    expect(errText(new WireError('some_new_code', 'the daemon explained itself'))).toBe(
      'the daemon explained itself',
    )
  })

  // 🪤 The daemon's detail outranks any generic line kept here, and getting
  // this backwards is worse than the bug being fixed. An earlier version of
  // errText keyed on the code first, so the one sentence that tells an operator
  // why the restart button did nothing was replaced by "terva hit an unexpected
  // error". Both codes below have an entry in bareCodeText.
  it('prefers the daemon explanation over a generic line for the same code', () => {
    expect(errText(new WireError(WireCodes.internal, 'restart: cannot self-restart a `go run` build'))).toBe(
      'restart: cannot self-restart a `go run` build',
    )
    expect(errText(new WireError(WireCodes.notFound, 'no card named mieli'))).toBe('no card named mieli')
  })

  it('uses the generic line only when the code arrives bare', () => {
    // detail empty, and detail === code, are both "the daemon said nothing".
    expect(errText(new WireError(WireCodes.internal, ''))).toBe('terva hit an unexpected error')
    expect(errText(new WireError(WireCodes.internal, 'internal'))).toBe('terva hit an unexpected error')
  })
})

describe('isWireCode', () => {
  it('matches on the code, not the prose', () => {
    const e = new WireError(ClientCodes.connectionClosed, 'connection closed')
    expect(isWireCode(e, ClientCodes.connectionClosed)).toBe(true)
    // The whole point: rewording the detail must not change the answer.
    const reworded = new WireError(ClientCodes.connectionClosed, 'socket closed (1006)')
    expect(isWireCode(reworded, ClientCodes.connectionClosed)).toBe(true)
  })

  it('is false for a plain Error carrying the same words', () => {
    expect(isWireCode(new Error('connection closed'), ClientCodes.connectionClosed)).toBe(false)
  })
})

describe('restartRejection', () => {
  // A SUCCESSFUL restart can race the ack, surfacing as a closed connection.
  // Swallowing it is the point; showing it would put an error toast on every
  // working restart.
  it('swallows the expected close race', () => {
    expect(restartRejection(new WireError(ClientCodes.connectionClosed, 'connection closed'))).toBeNull()
  })

  it('still swallows it when the close message is reworded', () => {
    // This is the regression the code check exists to prevent: the old version
    // compared `msg === 'connection closed'`, so adding the socket close code
    // to that string turned every successful restart into an error toast.
    expect(restartRejection(new WireError(ClientCodes.connectionClosed, 'socket closed (1006)'))).toBeNull()
  })

  it('surfaces a real failure as readable text', () => {
    const msg = restartRejection(new WireError(WireCodes.unsupported, 'restart is not supported on this platform'))
    expect(msg).toBe('restart is not supported on this platform')
    expect(msg).not.toMatch(/^Error:/)
  })
})

// The TS code table is a hand-written copy of the Go constants. Read the Go
// source rather than restating it — restating it here would be a third copy,
// which is the shape this whole finding is about.
const repoRoot = join(__dirname, '..', '..', '..', '..', '..', '..', '..')

function goWireCodes(): string[] {
  const src = readFileSync(join(repoRoot, 'packages/agent/ctrlproto/wire.go'), 'utf8')
  return [...src.matchAll(/\bCode[A-Za-z]+\s*=\s*"([a-z_]+)"/g)].map((m) => m[1])
}

describe('wire code parity', () => {
  it('reads plausible codes out of Go', () => {
    // Guards the guard: a regex matching nothing would make the next
    // assertion pass vacuously.
    const go = goWireCodes()
    expect(go.length).toBeGreaterThan(5)
    expect(go).toContain('busy')
  })

  it('every Go wire code has a TS name', () => {
    const mine = new Set(Object.values(WireCodes) as string[])
    const missing = goWireCodes().filter((c) => !mine.has(c))
    expect(
      missing,
      'ctrlproto/wire.go declares codes the web client cannot name, so errText cannot give them ' +
        'a sentence and isWireCode cannot branch on them',
    ).toEqual([])
  })

  it('client codes cannot collide with wire codes', () => {
    const wire = new Set(goWireCodes())
    for (const c of Object.values(ClientCodes) as string[]) {
      expect(wire.has(c), `${c} collides with a daemon code`).toBe(false)
    }
  })
})
