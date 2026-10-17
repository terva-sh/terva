// @vitest-environment happy-dom
import { cleanup, renderHook } from '@testing-library/preact'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TalkootCard, WireEvent } from '../../platform/ctrlproto/types'
import { TalkootHub } from '../../platform/talkoot/hub'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { TalkootSummary, Verb } from '../../platform/ctrlproto/types'
import { noticeLine, noticeSupport, useCardNotices, useTalkootNotify, useTeamNotices } from './notify'

const ask: TalkootCard = { session: 's-writer', member: 'writer', kind: 'ask', id: 'q1', ask: { id: 'q1', questions: [] } as never }
const permission: TalkootCard = {
  session: 's-lead',
  member: 'lead',
  kind: 'permission',
  id: 'c1',
  permission: { tool: 'bash', args: { command: 'rm -rf secret-dir' } } as never,
}
const inbox = (card: TalkootCard): WireEvent => ({ type: 'talkoot_inbox', talkoot: { id: 'crew', card } }) as WireEvent

let posted: [string, string][]
let hidden = true

beforeEach(() => {
  posted = []
  hidden = true
  localStorage.clear()
  ;(window as unknown as { tervaNotify?: unknown }).tervaNotify = (title: string, message: string) => {
    posted.push([title, message])
    return Promise.resolve()
  }
  vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  delete (window as unknown as { tervaNotify?: unknown }).tervaNotify
})

describe('card notices', () => {
  it('names the team and the member, and never the card body', () => {
    const hub = new TalkootHub()
    renderHook(() => useCardNotices(hub, 'crew', 'Crew', true, []))
    hub.dispatch('#talkoot:crew', inbox(permission))
    expect(posted).toEqual([['Crew', 'lead asks you to approve a tool call']])
    expect(posted[0].join(' ')).not.toContain('secret-dir')
  })

  it('posts one notice per card, and none for a card the team shows', () => {
    const hub = new TalkootHub()
    renderHook(() => useCardNotices(hub, 'crew', 'Crew', true, [permission]))
    hub.dispatch('#talkoot:crew', inbox(permission))
    hub.dispatch('#talkoot:crew', inbox(ask))
    hub.dispatch('#talkoot:crew', inbox(ask))
    expect(posted).toEqual([['Crew', 'writer asks you a question']])
  })

  it('stays quiet while off, and while the page is in front', () => {
    const hub = new TalkootHub()
    const { rerender } = renderHook(({ on }) => useCardNotices(hub, 'crew', 'Crew', on, []), { initialProps: { on: false } })
    hub.dispatch('#talkoot:crew', inbox(ask))
    expect(posted).toEqual([])
    rerender({ on: true })
    hidden = false
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    hub.dispatch('#talkoot:crew', inbox(permission))
    expect(posted).toEqual([])
  })

  it('stops when the person turns it off', () => {
    const hub = new TalkootHub()
    const { rerender } = renderHook(({ on }) => useCardNotices(hub, 'crew', 'Crew', on, []), { initialProps: { on: true } })
    rerender({ on: false })
    hub.dispatch('#talkoot:crew', inbox(ask))
    expect(posted).toEqual([])
  })

  it('a kickoff card names no member', () => {
    expect(noticeLine({ session: '', member: '', kind: 'kickoff', id: 'kickoff' })).toBe('The team waits for you to start the introductions')
  })
})

describe('notices for the teams that are not open', () => {
  const team = (id: string, state: TalkootSummary['state']): TalkootSummary => ({ id, running: true, title: id.toUpperCase(), state })
  const client = () =>
    fakeClient({
      respond: (method: Verb, params: unknown) =>
        method === 'talkoot.inbox' && (params as { id: string }).id === 'docs'
          ? { cards: [{ ...ask, member: 'old', at: '2026-10-15T10:00:00Z' }, { ...permission, member: 'editor', at: '2026-10-15T11:00:00Z' }] }
          : { cards: [] },
    })

  it('posts once when another team turns needs-you, naming its newest card', async () => {
    const c = client()
    const { rerender } = renderHook(({ teams }) => useTeamNotices(c, teams, 'crew', true), {
      initialProps: { teams: [team('crew', 'online'), team('docs', 'busy')] },
    })
    rerender({ teams: [team('crew', 'needs-you'), team('docs', 'needs-you')] })
    await vi.waitFor(() => expect(posted).toEqual([['DOCS', 'editor asks you to approve a tool call']]))
    rerender({ teams: [team('crew', 'needs-you'), team('docs', 'needs-you')] })
    await new Promise((r) => setTimeout(r, 20))
    expect(posted).toHaveLength(1)
  })

  it('drops a notice whose inbox read settles after the person opts out, comes to the front, or leaves', async () => {
    const settle: (() => void)[] = []
    const c = fakeClient({
      respond: () =>
        new Promise((resolve) => {
          settle.push(() => resolve(answer))
        }),
    })
    let answer: { cards: TalkootCard[] } = { cards: [permission] }
    const turn = async (
      after: (r: { rerender: (p: { on: boolean; open: string; state?: TalkootSummary['state'] }) => void; unmount: () => void }) => void,
    ) => {
      settle.length = 0
      hidden = true
      vi.restoreAllMocks()
      vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden)
      const docs = (state: TalkootSummary['state']) => [team('docs', state)]
      const r = renderHook(({ on, open, teams }) => useTeamNotices(c, teams, open, on), {
        initialProps: { on: true, open: '', teams: docs('busy') },
      })
      r.rerender({ on: true, open: '', teams: docs('needs-you') })
      await vi.waitFor(() => expect(settle).toHaveLength(1))
      after({ rerender: (p) => r.rerender({ on: p.on, open: p.open, teams: docs(p.state ?? 'needs-you') }), unmount: r.unmount })
      settle[0]()
      await new Promise((res) => setTimeout(res, 20))
      r.unmount()
    }
    await turn(({ rerender }) => rerender({ on: false, open: '' }))
    await turn(({ rerender }) => rerender({ on: true, open: 'docs' }))
    await turn(() => {
      hidden = false
      vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    })
    await turn(({ unmount }) => unmount())
    // The team stops waiting while the read is open.
    await turn(({ rerender }) => rerender({ on: true, open: '', state: 'busy' }))
    // The read finds the card answered already.
    answer = { cards: [] }
    await turn(() => {})
    answer = { cards: [permission] }
    expect(posted).toEqual([])
    // The control: the same read with nothing changed posts.
    await turn(() => {})
    expect(posted).toEqual([['DOCS', 'lead asks you to approve a tool call']])
  })

  it('stays quiet on the first list, while off, and in front', async () => {
    const c = client()
    const { rerender } = renderHook(({ teams, on }) => useTeamNotices(c, teams, '', on), {
      initialProps: { teams: [team('docs', 'needs-you')], on: true },
    })
    rerender({ teams: [team('docs', 'busy')], on: false })
    rerender({ teams: [team('docs', 'needs-you')], on: false })
    rerender({ teams: [team('docs', 'busy')], on: true })
    hidden = false
    vi.spyOn(document, 'hasFocus').mockReturnValue(true)
    rerender({ teams: [team('docs', 'needs-you')], on: true })
    await new Promise((r) => setTimeout(r, 20))
    expect(posted).toEqual([])
  })
})

describe('the notify choice', () => {
  it('is off until the person turns it on, and remembers it', async () => {
    expect(noticeSupport()).toBe('desktop')
    const { result } = renderHook(() => useTalkootNotify())
    expect(result.current[0]).toBe(false)
    await result.current[1](true)
    await vi.waitFor(() => expect(result.current[0]).toBe(true))
    expect(localStorage.getItem('terva_talkoot_notify')).toBe('on')
    await result.current[1](false)
    await vi.waitFor(() => expect(result.current[0]).toBe(false))
    expect(localStorage.getItem('terva_talkoot_notify')).toBeNull()
  })

  it('stays off in a browser that refuses the permission', async () => {
    delete (window as unknown as { tervaNotify?: unknown }).tervaNotify
    const fake = Object.assign(function () {}, { permission: 'default', requestPermission: () => Promise.resolve('denied') })
    vi.stubGlobal('Notification', fake)
    try {
      expect(noticeSupport()).toBe('browser')
      const { result } = renderHook(() => useTalkootNotify())
      await result.current[1](true)
      await vi.waitFor(() => expect(result.current[2]).toBe(true))
      expect(result.current[0]).toBe(false)
      expect(localStorage.getItem('terva_talkoot_notify')).toBeNull()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
