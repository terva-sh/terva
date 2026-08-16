import { describe, expect, it } from 'vitest'

import type { WireEvent } from '../ctrlproto/types'
import { emptySessionState, reduceSession, type SessionState } from './session'

// The three drifts this reducer exists to end each get a test by name, because
// each shipped once already and each was invisible until a user hit it.

const ev = (e: unknown) => e as WireEvent

function snapshot(over: Record<string, unknown> = {}): WireEvent {
  return ev({
    type: 'snapshot',
    snapshot: {
      epoch: 1,
      base: 0,
      total: 1,
      busy: false,
      messages: [{ role: 'assistant', content: [{ type: 'text', text: 'hello' }] }],
      ...over,
    },
  })
}

function fold(events: WireEvent[], from: SessionState = emptySessionState): SessionState {
  return events.reduce(reduceSession, from)
}

// An empty transcript is two states, and nothing else in here separates them.
// The subscribe is fire-and-forget and the snapshot arrives later as an event,
// so Stage rendered "Say something to begin." over a session with a full history
// until it landed. `loaded` is the seam that tells "no messages" from "no
// answer"; both apps read it to choose a placeholder over an empty state.
describe('reduceSession — loaded', () => {
  it('starts unloaded, because an initial value is not an answer', () => {
    expect(emptySessionState.loaded).toBe(false)
  })

  it('becomes loaded on the snapshot — the only event that carries the whole transcript', () => {
    expect(fold([snapshot()]).loaded).toBe(true)
  })

  // A snapshot may legitimately carry epoch 0, which is why win.epoch cannot
  // stand in for this flag — the obvious shortcut, and a wrong one.
  it('becomes loaded even on an empty snapshot at epoch 0', () => {
    const s = fold([snapshot({ epoch: 0, total: 0, messages: [] })])
    expect(s.loaded).toBe(true)
    expect(s.items).toHaveLength(0)
    expect(s.win.epoch).toBe(0)
  })

  // A delta says one message changed; it does not say we hold all of them. A
  // turn arriving before the snapshot (or after a missed one) must not be read
  // as the transcript having been answered.
  it('stays unloaded through events that are not snapshots', () => {
    expect(fold([ev({ type: 'turn_start' }), ev({ type: 'error', error: 'boom' }), ev({ type: 'done' })]).loaded).toBe(false)
  })

  it('stays loaded once loaded', () => {
    expect(fold([snapshot(), ev({ type: 'turn_start' })]).loaded).toBe(true)
  })
})

describe('reduceSession — busy', () => {
  it('raises busy on turn_start and clears it on turn_end', () => {
    expect(fold([ev({ type: 'turn_start' })]).busy).toBe(true)
    expect(fold([ev({ type: 'turn_start' }), ev({ type: 'turn_end' })]).busy).toBe(false)
  })

  // `done` is the second clearing path. Before it existed, busy cleared only on
  // the end-of-turn snapshot, so a dropped frame wedged the composer at
  // "thinking…" permanently — every later send silently swallowed.
  it('also clears busy on done, not only on the snapshot', () => {
    expect(fold([ev({ type: 'turn_start' }), ev({ type: 'done' })]).busy).toBe(false)
  })

  // DRIFT #3: Stage folded the error row and left busy true. The wedge again,
  // reached by a different door.
  it('clears busy on an error, and still folds the row', () => {
    const s = fold([ev({ type: 'turn_start' }), ev({ type: 'error', error: 'boom' })])
    expect(s.busy).toBe(false)
    expect(s.items.length).toBeGreaterThan(0)
  })

  it('takes busy from the snapshot authoritatively', () => {
    expect(fold([ev({ type: 'turn_start' }), snapshot({ busy: false })]).busy).toBe(false)
    expect(fold([snapshot({ busy: true })]).busy).toBe(true)
  })
})

describe('reduceSession — pending prompts', () => {
  const perm = { call_id: 'c1', tool: 'bash', preview: 'ls' }

  // DRIFT #2: Stage ignored these event types entirely, so a turn that reached
  // for a gated tool parked on the daemon with nothing on screen — from the
  // user's side, indistinguishable from "I sent a message and nothing happened".
  it('holds a permission request until it is resolved', () => {
    const s = fold([ev({ type: 'permission_request', permission: perm })])
    expect(s.permission).toEqual(perm)
    expect(fold([ev({ type: 'permission_resolved', resolved: { call_id: 'c1' } })], s).permission).toBeNull()
  })

  // Another client (the panel, a phone) may answer a DIFFERENT prompt. A stale
  // id must not clear the newer one still in front of this user.
  it('ignores a resolution for a prompt it is not showing', () => {
    const s = fold([ev({ type: 'permission_request', permission: perm })])
    const after = fold([ev({ type: 'permission_resolved', resolved: { call_id: 'other' } })], s)
    expect(after.permission).toEqual(perm)
    expect(after).toBe(s) // unchanged state is returned identically, so no re-render
  })

  it('holds and clears an ask the same way', () => {
    const askEv = ev({ type: 'ask_request', ask: { ask_id: 'a1', question: 'which?' } })
    const s = fold([askEv])
    expect(s.ask).not.toBeNull()
    expect(fold([ev({ type: 'ask_resolved', resolved: { ask_id: 'other' } })], s).ask).not.toBeNull()
    expect(fold([ev({ type: 'ask_resolved', resolved: { ask_id: 'a1' } })], s).ask).toBeNull()
  })

  // The self-heal: a re-subscribe or reload recovers a prompt whose event was
  // missed, because the snapshot carries whatever is still pending.
  it('recovers a pending prompt from a snapshot', () => {
    const s = fold([snapshot({ permissions: [perm], asks: [{ ask_id: 'a9', question: 'q' }] })])
    expect(s.permission).toEqual(perm)
    expect(s.ask?.ask_id).toBe('a9')
  })

  it('clears a pending prompt the snapshot no longer carries', () => {
    const s = fold([ev({ type: 'permission_request', permission: perm }), snapshot()])
    expect(s.permission).toBeNull()
  })
})

describe('reduceSession — the held window', () => {
  // A merge KEEPS what sits above the incoming base, so the held base is the
  // LOWER of the two. Taking the snapshot's base instead silently discards
  // paged-in history at the end of every turn — the bug mergeSnapshot exists for,
  // reachable again through the window bookkeeping around it.
  it('keeps a lower held base when the epoch is unchanged', () => {
    const seeded: SessionState = { ...emptySessionState, win: { epoch: 1, base: 10, total: 40 } }
    expect(fold([snapshot({ epoch: 1, base: 30, total: 40 })], seeded).win).toEqual({
      epoch: 1,
      base: 10,
      total: 40,
    })
  })

  // A new epoch means the transcript was rewritten; the old base describes a
  // transcript that no longer exists, so the snapshot's base wins.
  it('takes the snapshot base when the epoch changed', () => {
    const seeded: SessionState = { ...emptySessionState, win: { epoch: 1, base: 10, total: 40 } }
    expect(fold([snapshot({ epoch: 2, base: 30, total: 35 })], seeded).win).toEqual({
      epoch: 2,
      base: 30,
      total: 35,
    })
  })
})

describe('reduceSession — session info', () => {
  // A live model switch rides session_updated, not a snapshot (those land only at
  // a turn's end). Merging is what makes the picker reflect a switch immediately
  // instead of staying stale until the next turn.
  it('merges session_updated over the info it holds', () => {
    const s = fold([
      snapshot({ session: { id: 's1', title: 'One', model: 'opus', messages: 1 } }),
      ev({ type: 'session_updated', info: { id: 's1', model: 'haiku' } }),
    ])
    expect(s.info).toMatchObject({ id: 's1', title: 'One', model: 'haiku' })
  })

  it('accepts session_updated before any snapshot has landed', () => {
    expect(fold([ev({ type: 'session_updated', info: { id: 's1', model: 'opus' } })]).info).toMatchObject({
      id: 's1',
      model: 'opus',
    })
  })
})

describe('reduceSession — identity', () => {
  // Returning the same object when nothing changed lets a host skip a render.
  // The transcript re-renders on every token delta; a needless copy here is a
  // needless re-render of the whole screen.
  it('returns the same state for an event that changes nothing', () => {
    const s = fold([snapshot()])
    expect(reduceSession(s, ev({ type: 'session_updated' }))).toBe(s)
    expect(reduceSession(s, ev({ type: 'snapshot' }))).toBe(s)
    expect(reduceSession(s, ev({ type: 'turn_end' }))).toBe(s) // already idle
  })

  it('does not mutate the state it was given', () => {
    const before = fold([snapshot()])
    const snapshotOfState = JSON.stringify({ ...before, msgMarks: [...before.msgMarks] })
    reduceSession(before, ev({ type: 'turn_start' }))
    reduceSession(before, ev({ type: 'error', error: 'x' }))
    expect(JSON.stringify({ ...before, msgMarks: [...before.msgMarks] })).toBe(snapshotOfState)
  })
})

// Live reasoning is held OUTSIDE items on purpose: it is shown while a turn
// runs and dropped when it ends. Anything that folded it into the transcript
// would render it twice — the assembled summary arrives again on the finished
// message — and would persist a row for something deliberately ephemeral.
describe('live reasoning', () => {
  const delta = (d: string) => ev({ type: 'reasoning_delta', delta: d })

  it('accumulates deltas without touching items', () => {
    const s = fold([delta('**Reading '), delta('the config**')])
    expect(s.reasoning).toBe('**Reading the config**')
    expect(s.items).toHaveLength(0)
  })

  it('drops the previous segment when the next one starts', () => {
    const s = fold([delta('**First**'), ev({ type: 'assistant_start' }), delta('**Second**')])
    expect(s.reasoning).toBe('**Second**')
  })

  for (const ending of ['turn_end', 'done', 'error']) {
    it(`clears on ${ending}, so no thought outlives the work it narrated`, () => {
      const s = fold([ev({ type: 'turn_start' }), delta('**Still going**'), ev({ type: ending })])
      expect(s.reasoning).toBe('')
      expect(s.busy).toBe(false)
    })
  }
})
