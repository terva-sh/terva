// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { TalkootEnvelope, TalkootLine, TalkootView, Verb } from '../../platform/ctrlproto/types'
import { TalkootHub } from '../../platform/talkoot/hub'
import { mentionAt } from '../../ui/talkoot/TalkootComposer'
import { Shell } from './Shell'

const crew: TalkootView = {
  id: 'crew',
  name: 'crew',
  home: '/w',
  members: [
    { id: 'lead', role: 'coordinator', title: 'Lead', status: { member: 'lead' } },
    {
      id: 'writer',
      role: 'specialist',
      title: 'Writer',
      driver: 'native',
      session: 's-writer',
      status: { member: 'writer', presence: 'working', working: true, tool: 'grep', spend_usd: 0.25, turns: 3 },
    },
    { id: 'checker', role: 'specialist', status: { member: 'checker' } },
  ],
}

let n = 0
// gets counts the roster reads. The checker's first turn opens its session
// after the first read, as on a real daemon.
let gets = 0
const env = (from: string, to: string[], s: number, body: string, root = 'c1', refs?: string[]): TalkootLine => {
  const at = `2026-10-10T10:00:${String(s).padStart(2, '0')}Z`
  const e: TalkootEnvelope = { id: `e${++n}`, talkoot: 'crew', from, to, kind: 'message', body, chain: { root, hops: 1 }, at, refs }
  return { type: 'envelope', at, envelope: e }
}

function respond(method: Verb, params: unknown): unknown {
  switch (method) {
    case 'talkoot.list':
      return { talkoots: [{ id: 'crew', name: 'crew', running: true, color: '#3366cc', state: 'online' }] }
    case 'talkoot.get':
      gets++
      return gets === 1 ? crew : { ...crew, members: crew.members.map((m) => (m.id === 'checker' ? { ...m, session: 's-checker' } : m)) }
    case 'talkoot.room':
      return {
        total: 5,
        lines: [
          env('human:sothr', ['lead'], 1, 'Refresh the install page'),
          env('lead', ['writer'], 2, 'Please take this'),
          env('writer', ['checker'], 3, 'Check my branch'),
          env('checker', ['lead'], 4, 'Looks right'),
          env('lead', ['human:sothr'], 6, 'Done, please review', 'c1', ['note:lead/summary.md']),
        ],
      }
    case 'talkoot.inbox':
      return {
        cards: [
          {
            session: 's-writer',
            member: 'writer',
            kind: 'ask',
            id: 'a1',
            at: '2026-10-10T10:00:05Z',
            ask: { ask_id: 'a1', question: 'Keep the old URL?', options: ['Yes', 'No'] },
          },
        ],
      }
    case 'talkoot.notes':
      return { notes: [{ ref: 'note:writer/draft.md', member: 'writer', name: 'draft.md', size: 2048, at: '2026-10-10T10:00:07Z' }] }
    case 'talkoot.ref': {
      const ref = (params as { ref: string }).ref
      return { ref, text: `the text of ${ref}`, size: 20 }
    }
  }
  return {}
}

beforeEach(() => {
  gets = 0
  localStorage.clear()
  localStorage.setItem('terva_talkoot_person', 'sothr')
})
afterEach(() => cleanup())

const open = async () => {
  const client = fakeClient({ respond })
  render(<Shell client={client} hub={new TalkootHub()} generation={1} />)
  await screen.findByText('Refresh the install page')
  return client
}

describe('the Talkoot app', () => {
  it('opens on the room, with one chain of teammate messages folded into a thread', async () => {
    await open()
    expect(document.querySelector('.tka-main-head strong')!.textContent).toBe('# room')
    const threads = document.querySelectorAll('.tka-thread')
    expect(threads).toHaveLength(1)
    expect(threads[0].querySelector('summary')!.textContent).toContain('3 messages')
    expect(threads[0].querySelector('.tka-thread-who')!.textContent).toBe('lead, writer, checker')
    // The person's post and the reply to the person stand outside the thread.
    expect(screen.getByText('Done, please review').closest('.tka-thread')).toBeNull()
  })

  it('puts a card into the conversation at the time it opened, and counts it above the composer', async () => {
    await open()
    await screen.findByText('Keep the old URL?')
    const log = document.querySelector('.tka-log')!
    const order = [...log.children].map((el) => (el.matches('.tka-card') ? 'card' : el.matches('.tka-thread') ? 'thread' : 'line'))
    expect(order).toEqual(['line', 'thread', 'card', 'line'])
    expect(document.querySelector('.tka-strip')!.textContent).toContain('1 card waits for you')
  })

  it('opens a member thread from the rail, and posts to that member', async () => {
    const client = await open()
    fireEvent.click(screen.getByRole('button', { name: /^Writer/ }))
    expect(document.querySelector('.tka-main-head strong')!.textContent).toBe('Writer')
    // Only the person's exchanges with the member, and the member's own card.
    expect(screen.queryByText('Refresh the install page')).toBeNull()
    expect(screen.getByText('Keep the old URL?')).toBeTruthy()
    fireEvent.input(screen.getByLabelText('Message to the team'), { target: { value: 'How is it going?' } })
    fireEvent.submit(document.querySelector('.talkoot-composer')!)
    await waitFor(() => expect(client.last('talkoot.post')).toBeTruthy())
    expect(client.last('talkoot.post')!.params).toMatchObject({ id: 'crew', by: 'sothr', to: ['writer'] })
  })

  it('completes a member id after @ in the composer', async () => {
    await open()
    const box = screen.getByLabelText('Message to the team') as HTMLTextAreaElement
    box.value = 'ask @wr'
    box.setSelectionRange(7, 7)
    fireEvent.input(box)
    const option = await screen.findByRole('option', { name: '@writer' })
    fireEvent.mouseDown(option)
    await waitFor(() => expect(box.value).toBe('ask @writer '))
  })

  it('pauses a chain from its thread', async () => {
    const client = await open()
    fireEvent.click(document.querySelector('.tka-thread summary .btn')!)
    await waitFor(() => expect(client.last('talkoot.pause')).toBeTruthy())
    expect(client.last('talkoot.pause')!.params).toMatchObject({ id: 'crew', by: 'sothr', chain: 'c1' })
  })
})

describe('the side panel', () => {
  it("shows a member's card and work, with a link to the session in the control panel", async () => {
    await open()
    fireEvent.click(screen.getByRole('button', { name: /^Writer/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    const side = document.querySelector('.tka-side') as HTMLElement
    expect(side.querySelector('.talkoot-member-card')).toBeTruthy()
    fireEvent.click(screen.getByRole('tab', { name: 'Work' }))
    expect(side.textContent).toContain('grep')
    expect(side.textContent).toContain('$0.25')
    const link = screen.getByRole('link', { name: 'Open the session in the control panel' }) as HTMLAnchorElement
    expect(new URL(link.href).searchParams.get('session')).toBe('s-writer')
  })

  it("reads the roster again for a member whose session opened after the view's read", async () => {
    await open()
    fireEvent.click(screen.getByRole('button', { name: /^checker/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    fireEvent.click(screen.getByRole('tab', { name: 'Work' }))
    const link = (await screen.findByRole('link', { name: 'Open the session in the control panel' })) as HTMLAnchorElement
    expect(new URL(link.href).searchParams.get('session')).toBe('s-checker')
    expect(gets).toBe(2)
  })

  it('lists the notes in the room, and opens one in place', async () => {
    const client = await open()
    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    // The room has no member, so the panel opens on the notes.
    expect(screen.queryByRole('tab', { name: 'Member' })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: /draft\.md/ }))
    await screen.findByText('the text of note:writer/draft.md')
    expect(client.last('talkoot.notes')!.params).toEqual({ id: 'crew' })
    expect(screen.getByRole('tab', { name: 'File' }).getAttribute('aria-selected')).toBe('true')
  })

  it('opens a reference chip in the side panel, not in the line', async () => {
    const client = await open()
    fireEvent.click(screen.getByRole('button', { name: 'note:lead/summary.md' }))
    await screen.findByText('the text of note:lead/summary.md')
    expect(client.last('talkoot.ref')!.params).toEqual({ id: 'crew', ref: 'note:lead/summary.md' })
    expect(document.querySelector('.tka-log .talkoot-ref-body')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Close the side panel' }))
    expect(document.querySelector('.tka-side')).toBeNull()
  })
})

describe('the composer', () => {
  it('replaces the whole name when the caret sits inside it', async () => {
    await open()
    const box = screen.getByLabelText('Message to the team') as HTMLTextAreaElement
    box.value = 'ask @wrxyz now'
    box.setSelectionRange(7, 7)
    fireEvent.input(box)
    fireEvent.mouseDown(await screen.findByRole('option', { name: '@writer' }))
    await waitFor(() => expect(box.value).toBe('ask @writer  now'))
  })

  it('opens the list again for a name typed again where Escape closed one', async () => {
    await open()
    const box = screen.getByLabelText('Message to the team') as HTMLTextAreaElement
    const type = (value: string) => {
      box.value = value
      box.setSelectionRange(value.length, value.length)
      fireEvent.input(box)
    }
    type('@w')
    await screen.findByRole('option', { name: '@writer' })
    fireEvent.keyDown(box, { key: 'Escape' })
    expect(screen.queryByRole('listbox')).toBeNull()
    type('')
    type('@w')
    expect(await screen.findByRole('option', { name: '@writer' })).toBeTruthy()
  })
})

describe('the screen with no team', () => {
  it('opens the rail as a drawer, so the name and the panel link stay reachable', async () => {
    const client = fakeClient({ respond: (m) => (m === 'talkoot.list' ? { talkoots: [] } : {}) })
    render(<Shell client={client} hub={new TalkootHub()} generation={1} />)
    await screen.findByText('No talkoot runs in this workspace yet.')
    const shell = document.querySelector('.tka')!
    expect(shell.classList.contains('drawer-open')).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Teams' }))
    expect(shell.classList.contains('drawer-open')).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Close the menu' }))
    expect(shell.classList.contains('drawer-open')).toBe(false)
  })
})

describe('the first-run page', () => {
  const respond = (m: string) => {
    if (m === 'talkoot.list') return { talkoots: [] }
    if (m === 'talkoot.templates') return { templates: [{ name: 'review-pair', title: 'Review pair', description: 'A developer and a reviewer.', source: 'builtin', members: 2 }] }
    return {}
  }

  it('names the workspace, asks for the name in place, and opens the form on the template picked', async () => {
    localStorage.removeItem('terva_talkoot_person')
    const client = fakeClient({ respond: (m) => respond(m) })
    render(<Shell client={client} hub={new TalkootHub()} generation={1} workspace="/home/me/src/app" />)
    await screen.findByText('Start your first team')
    expect(screen.getByText(/Teams here belong to \/home\/me\/src\/app/)).toBeTruthy()
    expect(screen.getByText(/You need one to post, answer, and pause/)).toBeTruthy()
    const name = screen.getByLabelText('Your name in the room', { selector: '.talkoot-first-run input' }) as HTMLInputElement
    fireEvent.input(name, { target: { value: 'sothr' } })
    fireEvent.keyDown(name, { key: 'Enter' })
    await waitFor(() => expect(localStorage.getItem('terva_talkoot_person')).toBe('sothr'))

    fireEvent.click(await screen.findByRole('button', { name: 'Review pair' }))
    await screen.findByText('New team from a template')
    await waitFor(() => expect((document.querySelector('.talkoot-new select') as HTMLSelectElement).value).toBe('review-pair'))
  })

  it('opens the builder from the first-run page', async () => {
    const client = fakeClient({ respond: (m) => respond(m) })
    render(<Shell client={client} hub={new TalkootHub()} generation={1} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Build a team member by member' }))
    expect(await screen.findByRole('heading', { name: 'Build a team' })).toBeTruthy()
  })
})

describe('mentionAt', () => {
  it('finds the name the caret is in, and nothing inside a word', () => {
    expect(mentionAt('hi @wr', 6)).toEqual({ start: 3, prefix: 'wr' })
    expect(mentionAt('@', 1)).toEqual({ start: 0, prefix: '' })
    expect(mentionAt('mail@host', 9)).toBeNull()
    expect(mentionAt('hi @wr there', 12)).toBeNull()
  })
})
