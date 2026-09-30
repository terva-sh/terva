// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { TalkootPreview, TalkootView, TaskInfo, Verb, WireEvent } from '../../platform/ctrlproto/types'
import { TalkootHub } from '../../platform/talkoot/hub'
import { NewTalkoot } from './NewTalkoot'
import { TalkootScreen } from './TalkootScreen'
import { recipients } from './TalkootComposer'
import { TalkootTeam } from './TalkootTeam'
import { RoomExchange, RoomLine } from './RoomLine'
import { useTalkootPerson } from './person'
import { eyeColor, MARK_SHAPES } from '../../platform/talkoot/marks'
import { drawnShapes } from './MemberMark'
import { LOAD_RETRY_MAX_MS, REFRESH_RETRY_MS } from './useTalkoot'
import { WORKER_POLL_MS } from './WorkerEvents'
import type { TalkootLine } from '../../platform/ctrlproto/types'

const crew: TalkootView = {
  id: 'crew',
  name: 'crew',
  home: '/w',
  members: [
    { id: 'arkkitehti', role: 'planner', title: 'Planner', status: { member: 'arkkitehti' } },
    { id: 'mieli', role: 'coordinator', title: 'Lead', session: 's-lead', status: { member: 'mieli' } },
  ],
}

function teamRespond(method: Verb): unknown {
  switch (method) {
    case 'talkoot.get':
      return crew
    case 'talkoot.room':
      return {
        total: 1,
        lines: [
          {
            type: 'envelope',
            at: '2026-09-27T10:00:00Z',
            envelope: { id: 'e1', talkoot: 'crew', from: 'mieli', to: ['human:sothr'], kind: 'message', body: 'Hello from the lead', chain: { root: 'e1', hops: 0 }, at: '2026-09-27T10:00:00Z' },
          },
        ],
      }
    case 'talkoot.inbox':
      return { cards: [{ session: '', member: '', kind: 'kickoff', id: 'kickoff', kickoff: { state: 'waiting', members: [], estimate_usd: 0.42 } }] }
  }
  return {}
}

const teamClient = () => fakeClient({ respond: teamRespond })

beforeEach(() => localStorage.clear())
afterEach(() => cleanup())

describe('the talkoot team view', () => {
  it('opens on the coordinator, posts as the person, and resubscribes on reconnect', async () => {
    const client = teamClient()
    const hub = new TalkootHub()
    const view = render(<TalkootTeam client={client} hub={hub} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    // The coordinator's conversation is the opening view, and it is pinned first.
    expect(document.querySelector('.talkoot-main-head strong')!.textContent).toBe('Lead')
    const names = [...document.querySelectorAll('.talkoot-member-row .talkoot-member-name')].map((e) => e.textContent)
    expect(names[0]).toContain('Lead')

    fireEvent.input(screen.getByLabelText('Message to the team'), { target: { value: 'Plan the schema' } })
    fireEvent.submit(document.querySelector('.talkoot-composer')!)
    await waitFor(() => expect(client.last('talkoot.post')).toBeTruthy())
    expect(client.last('talkoot.post')!.params).toEqual({ id: 'crew', by: 'sothr', body: 'Plan the schema', to: ['mieli'] })

    // A room event lands through the hub.
    hub.dispatch('#talkoot:crew', {
      type: 'talkoot_envelope',
      talkoot: {
        id: 'crew',
        line: {
          type: 'envelope',
          at: '2026-09-27T10:01:00Z',
          envelope: { id: 'e2', talkoot: 'crew', from: 'mieli', to: ['human:sothr'], kind: 'message', body: 'On it', chain: { root: 'e2', hops: 0 }, at: '2026-09-27T10:01:00Z' },
        },
      },
    } as WireEvent)
    await screen.findByText('On it')

    const subscribes = () => client.commands.filter((c) => c.method === 'subscribe' && c.sess === '#talkoot:crew').length
    expect(subscribes()).toBe(1)
    view.rerender(<TalkootTeam client={client} hub={hub} id="crew" generation={2} person="sothr" onOpenSession={() => {}} />)
    await waitFor(() => expect(subscribes()).toBe(2))
    // The reconnect reads the room again and repeats no line.
    await waitFor(() => expect(client.sent('talkoot.room')).toHaveLength(2))
    expect(screen.getAllByText('Hello from the lead')).toHaveLength(1)
  })

  it('runs the kickoff from the inbox, and hides every action until the person has a name', async () => {
    const client = teamClient()
    const view = render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="" onOpenSession={() => {}} />)
    await screen.findByText('The team waits for its kickoff')
    expect(screen.queryByLabelText('Message to the team')).toBeNull()
    expect(screen.queryByText('Pause the team')).toBeNull()

    view.rerender(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    fireEvent.click(await screen.findByText('Run the introductions'))
    await waitFor(() => expect(client.last('talkoot.kickoff')!.params).toEqual({ id: 'crew', by: 'sothr', skip: false }))
  })
})

describe('a worker member stopped while idle', () => {
  it('says so in the sidebar until its next turn', async () => {
    const idle: TalkootView = {
      ...crew,
      members: [crew.members[0], { ...crew.members[1], driver: 'claude', status: { member: 'mieli', idle: true } }],
    }
    const hub = new TalkootHub()
    const client = fakeClient({ respond: (method: Verb) => (method === 'talkoot.get' ? idle : teamRespond(method)) })
    render(<TalkootTeam client={client} hub={hub} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText(/worker stopped while idle/)
    hub.dispatch('#talkoot:crew', {
      type: 'talkoot_status',
      talkoot: { id: 'crew', members: [{ member: 'mieli', working: true }] },
    } as WireEvent)
    await waitFor(() => expect(screen.queryByText(/worker stopped while idle/)).toBeNull())
  })
})

describe('a worker member\'s event view', () => {
  const worker: TalkootView = {
    ...crew,
    members: [crew.members[0], { ...crew.members[1], driver: 'claude', session: 'agent-7' }],
  }
  const task: TaskInfo = { id: 'agent-7', task: 'lead', status: 'running', activity: 'running Bash', turns: 3, tail: 'user: [talkoot crew] hello\nassistant: on it' }

  afterEach(() => vi.useRealTimers())

  it('reads the worker with talkoot.worker, not the session', async () => {
    const client = fakeClient({
      respond: (method: Verb) => (method === 'talkoot.get' ? worker : method === 'talkoot.worker' ? task : teamRespond(method)),
    })
    const opened: string[] = []
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={(s) => opened.push(s)} />)
    fireEvent.click(await screen.findByText('Worker events'))
    expect(await screen.findByText(/assistant: on it/)).toBeTruthy()
    expect(screen.getByText(/running Bash · 3 turns/)).toBeTruthy()
    expect(client.last('talkoot.worker')!.params).toEqual({ id: 'crew', member: 'mieli' })
    expect(screen.queryByText('Open session')).toBeNull()
    expect(opened).toEqual([])
  })

  it('reads again while it is open, and shows why a read fails', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let calls = 0
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return worker
        if (method === 'talkoot.worker') {
          calls++
          if (calls > 1) throw new Error('talkoot: worker agent-7 of member mieli is not in the swarm')
          return task
        }
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    fireEvent.click(await screen.findByText('Worker events'))
    expect(await screen.findByText(/assistant: on it/)).toBeTruthy()
    vi.advanceTimersByTime(WORKER_POLL_MS)
    expect(await screen.findByText(/is not in the swarm/)).toBeTruthy()
    // The last snapshot stays under the error.
    expect(screen.getByText(/assistant: on it/)).toBeTruthy()
    fireEvent.click(screen.getByText('Hide the worker events'))
    const after = calls
    vi.advanceTimersByTime(WORKER_POLL_MS * 3)
    await new Promise((r) => setTimeout(r, 10))
    expect(calls).toBe(after)
  })

  it('waits for a slow read rather than starting another', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let release: (tk: TaskInfo) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return worker
        if (method === 'talkoot.worker') return new Promise<TaskInfo>((r) => (release = r))
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    fireEvent.click(await screen.findByText('Worker events'))
    await waitFor(() => expect(client.sent('talkoot.worker')).toHaveLength(1))
    vi.advanceTimersByTime(WORKER_POLL_MS * 3)
    await new Promise((r) => setTimeout(r, 10))
    expect(client.sent('talkoot.worker')).toHaveLength(1)
    release(task)
    expect(await screen.findByText(/assistant: on it/)).toBeTruthy()
    vi.advanceTimersByTime(WORKER_POLL_MS)
    await waitFor(() => expect(client.sent('talkoot.worker')).toHaveLength(2))
  })

  it('keeps Open session for a native member', async () => {
    const client = fakeClient({ respond: teamRespond })
    const opened: string[] = []
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={(s) => opened.push(s)} />)
    fireEvent.click(await screen.findByText('Open session'))
    expect(opened).toEqual(['s-lead'])
    expect(screen.queryByText('Worker events')).toBeNull()
    expect(client.sent('talkoot.worker')).toHaveLength(0)
  })
})

describe('a worker member\'s approval', () => {
  // A worker member has no session of its own. Its card names the talkoot's
  // address, and the approval goes there.
  it('answers on the session the card names', async () => {
    const client = fakeClient({
      respond: (method: Verb) =>
        method === 'talkoot.inbox'
          ? {
              cards: [
                {
                  session: '#talkoot:crew',
                  member: 'arkkitehti',
                  kind: 'permission',
                  id: 'worker-agent-1-1',
                  permission: { call_id: 'worker-agent-1-1', tool: 'bash', preview: 'ls', agent: 'agent-1' },
                },
              ],
            }
          : teamRespond(method),
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    fireEvent.click(await screen.findByText('Allow'))
    await waitFor(() => expect(client.commands.find((c) => c.method === 'approve')).toBeTruthy())
    const sent = client.commands.find((c) => c.method === 'approve')!
    expect(sent.sess).toBe('#talkoot:crew')
    expect(sent.params).toEqual({ call_id: 'worker-agent-1-1', decision: { allow: true } })
  })
})

describe('the initial load', () => {
  // 🚨 An event that lands while the snapshot is in flight must survive it:
  // the inbox read replaces every card, and the roster read every status.
  it('applies events from the load window on top of the snapshot', async () => {
    let answerInbox: (v: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return crew
        if (method === 'talkoot.room') return { total: 0, lines: [] }
        if (method === 'talkoot.inbox') return new Promise((r) => (answerInbox = r))
        return {}
      },
    })
    const hub = new TalkootHub()
    render(<TalkootTeam client={client} hub={hub} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await waitFor(() => expect(client.sent('talkoot.inbox')).toHaveLength(1))
    const card = { session: 's-lead', member: 'mieli', kind: 'ask' as const, id: 'q1' }
    // The question is answered, and the member starts working, before the
    // stale inbox read comes back still holding the card.
    hub.dispatch('#talkoot:crew', { type: 'talkoot_inbox_resolved', talkoot: { id: 'crew', card } } as WireEvent)
    hub.dispatch('#talkoot:crew', { type: 'talkoot_status', talkoot: { id: 'crew', members: [{ member: 'mieli', working: true }] } } as WireEvent)
    answerInbox({ cards: [card] })
    await waitFor(() => expect(document.querySelector('.talkoot-member-row .presence-working')).toBeTruthy())
    expect(document.querySelector('.talkoot-inbox')).toBeNull()
  })
})

describe('a collapsed exchange', () => {
  it('keeps the chain pause', () => {
    const line = (id: string, from: string, to: string): TalkootLine => ({
      type: 'envelope',
      at: '2026-09-27T10:00:00Z',
      envelope: { id, talkoot: 'crew', from, to: [to], kind: 'message', body: id, chain: { root: 'c9', hops: 2 }, at: '2026-09-27T10:00:00Z' },
    })
    const onPause = vi.fn()
    render(<RoomExchange members={['a', 'b']} lines={[line('e1', 'a', 'b'), line('e2', 'b', 'a')]} onPauseChain={onPause} />)
    fireEvent.click(document.querySelector('.talkoot-exchange > summary button')!)
    expect(onPause).toHaveBeenCalledWith('c9')
  })
})

describe('recipients', () => {
  it('prefers an @member, then the open view, then the coordinator', () => {
    expect(recipients('@jev look', ['jev', 'helm'], 'helm')).toEqual(['jev'])
    expect(recipients('mail me@jev.io', ['jev'], '')).toEqual([])
    expect(recipients('hi', ['jev'], 'jev')).toEqual(['jev'])
    expect(recipients('@nobody hi', ['jev'], '')).toEqual([])
  })
})

describe('creating a talkoot from a template', () => {
  const preview = (drop: string[] = []): TalkootPreview => ({
    template: { name: 'repo:review', source: 'repo', members: 2 },
    id: 'rev',
    home: '/w',
    budget_usd_per_day: 6,
    members: [
      { id: 'lead', role: 'coordinator', driver: 'native', posture: 'plan', workspace: 'shared', available: true },
      ...(drop.includes('outside')
        ? []
        : [{ id: 'outside', role: 'specialist', driver: 'acp:gone', posture: 'plan', workspace: 'shared', available: false, problem: 'driver "acp:gone" is not installed here' }]),
    ],
    text: '---\n',
    digest: drop.length ? 'd-dropped' : 'd-full',
  })

  it('shows the preview, refuses to create until the unavailable member is dropped, and sends the digest', async () => {
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.templates') return { templates: [{ name: 'repo:review', title: 'Review', source: 'repo', members: 2 }] }
        if (method === 'talkoot.preview') return preview((params as { drop?: string[] }).drop ?? [])
        if (method === 'talkoot.create') return { id: 'rev', name: 'rev', home: '/w', members: [] }
        return {}
      },
    })
    let created = ''
    render(<NewTalkoot client={client} taken={[]} onCreated={(id) => (created = id)} onCancel={() => {}} />)
    await screen.findByText(/This template comes from the repository/)
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'rev' } })
    fireEvent.click(screen.getByText('Preview'))
    await screen.findByText('driver "acp:gone" is not installed here')
    expect((screen.getByText('Create the team') as HTMLButtonElement).disabled).toBe(true)

    fireEvent.click(screen.getByLabelText('Drop outside'))
    await waitFor(() => expect(client.last('talkoot.preview')!.params).toMatchObject({ drop: ['outside'] }))
    await waitFor(() => expect((screen.getByText('Create the team') as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(screen.getByText('Create the team'))
    await waitFor(() => expect(created).toBe('rev'))
    expect(client.last('talkoot.create')!.params).toEqual({ template: 'repo:review', id: 'rev', drop: ['outside'], digest: 'd-dropped' })
  })

  it('drops a preview that answers after the form changed', async () => {
    let answer: (v: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.templates') return { templates: [{ name: 'repo:review', title: 'Review', source: 'repo', members: 2 }] }
        if (method === 'talkoot.preview') return new Promise((r) => (answer = r))
        return {}
      },
    })
    render(<NewTalkoot client={client} taken={[]} onCreated={() => {}} onCancel={() => {}} />)
    await screen.findByText(/This template comes from the repository/)
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'rev' } })
    fireEvent.click(screen.getByText('Preview'))
    await waitFor(() => expect(client.sent('talkoot.preview')).toHaveLength(1))
    // The person renames the team while the preview for "rev" is in flight.
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'other' } })
    answer(preview())
    await new Promise((r) => setTimeout(r, 20))
    expect(document.querySelector('.talkoot-preview')).toBeNull()
    expect(screen.queryByText('Create the team')).toBeNull()
  })
})

describe('a failure or an edit mid-flight', () => {
  it('keeps the draft when a post fails', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.post') throw new Error('the room is closed')
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    const box = screen.getByLabelText('Message to the team') as HTMLTextAreaElement
    fireEvent.input(box, { target: { value: 'Plan the schema' } })
    fireEvent.submit(document.querySelector('.talkoot-composer')!)
    await screen.findByText('the room is closed')
    // The composer settles after the post does, so wait until it is ready to send again.
    await waitFor(() => expect((document.querySelector('.talkoot-composer button[type=submit]') as HTMLButtonElement).disabled).toBe(false))
    expect(box.value).toBe('Plan the schema')
  })

  it('leaves Preview usable after an edit drops a preview in flight', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.templates') return { templates: [{ name: 'coding', title: 'Coding', source: 'builtin', members: 6 }] }
        if (method === 'talkoot.preview') return new Promise(() => {})
        return {}
      },
    })
    render(<NewTalkoot client={client} taken={[]} onCreated={() => {}} onCancel={() => {}} />)
    await waitFor(() => expect(client.sent('talkoot.templates')).toHaveLength(1))
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'rev' } })
    fireEvent.click(screen.getByText('Preview'))
    await waitFor(() => expect(client.sent('talkoot.preview')).toHaveLength(1))
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'other' } })
    await waitFor(() => expect((screen.getByText('Preview') as HTMLButtonElement).disabled).toBe(false))
  })

  it('opens a new team even when the list read after it fails', async () => {
    let lists = 0
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.list') {
          lists++
          if (lists > 1) throw new Error('list failed')
          return { talkoots: [] }
        }
        if (method === 'talkoot.templates') return { templates: [{ name: 'coding', title: 'Coding', source: 'builtin', members: 6 }] }
        if (method === 'talkoot.preview') return { template: { name: 'coding', source: 'builtin', members: 1 }, id: 'rev', home: '/w', budget_usd_per_day: 20, members: [], text: '', digest: 'd' }
        if (method === 'talkoot.create') return { id: 'rev', name: 'rev', home: '/w', members: [] }
        return teamRespond(method)
      },
    })
    render(<TalkootScreen client={client} hub={new TalkootHub()} generation={1} onOpenSession={() => {}} onClose={() => {}} />)
    fireEvent.click(await screen.findByText('New team'))
    await waitFor(() => expect(client.sent('talkoot.templates')).toHaveLength(1))
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'rev' } })
    fireEvent.click(screen.getByText('Preview'))
    fireEvent.click(await screen.findByText('Create the team'))
    await waitFor(() => expect(client.last('talkoot.get')?.params).toEqual({ id: 'rev' }))
    expect(document.querySelector('.talkoot-new')).toBeNull()
  })
})

describe('what the person keeps across a page, a reload, and a create', () => {
  const pairLine = (id: string, from: string, to: string, at: string, hops = 1): TalkootLine => ({
    type: 'envelope',
    at,
    envelope: { id, talkoot: 'crew', from, to: [to], kind: 'message', body: `body ${id}`, chain: { root: 'c1', hops }, at },
  })

  it('offers the chain pause on the message that starts the chain', () => {
    const onPause = vi.fn()
    render(<RoomLine line={pairLine('e1', 'mieli', 'arkkitehti', '2026-09-27T10:00:00Z', 0)} onPauseChain={onPause} />)
    fireEvent.click(document.querySelector('.talkoot-line button')!)
    expect(onPause).toHaveBeenCalledWith('c1')
  })

  it('keeps an open exchange open when an older page extends it', async () => {
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.room') {
          if ((params as { before?: number }).before) return { total: 3, lines: [pairLine('e1', 'mieli', 'arkkitehti', '2026-09-27T10:00:00Z')] }
          return { total: 3, next: 1, lines: [pairLine('e2', 'arkkitehti', 'mieli', '2026-09-27T10:00:01Z'), pairLine('e3', 'mieli', 'arkkitehti', '2026-09-27T10:00:02Z')] }
        }
        if (method === 'talkoot.inbox') return { cards: [] }
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Room')
    fireEvent.click(screen.getByText('Room'))
    await waitFor(() => expect(document.querySelector('.talkoot-exchange')).toBeTruthy())
    const details = document.querySelector('.talkoot-exchange') as HTMLDetailsElement
    details.open = true
    fireEvent(details, new Event('toggle'))
    fireEvent.click(screen.getByText('Load earlier messages'))
    await waitFor(() => expect(document.querySelector('.talkoot-exchange summary')!.textContent).toContain('3 messages'))
    expect((document.querySelector('.talkoot-exchange') as HTMLDetailsElement).open).toBe(true)
  })

  it('removes a cleared name from storage', () => {
    let set: (n: string) => void = () => {}
    function Probe() {
      const [, s] = useTalkootPerson()
      set = s
      return null
    }
    render(<Probe />)
    set('sothr')
    expect(localStorage.getItem('terva_talkoot_person')).toBe('sothr')
    set('')
    expect(localStorage.getItem('terva_talkoot_person')).toBeNull()
  })

  it('does not let a list read from before a create remove the new team', async () => {
    let answerFirst: (v: unknown) => void = () => {}
    let lists = 0
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.list') {
          lists++
          if (lists === 1) return { talkoots: [] }
          if (lists === 2) return new Promise((r) => (answerFirst = r))
          return new Promise(() => {})
        }
        if (method === 'talkoot.templates') return { templates: [{ name: 'coding', title: 'Coding', source: 'builtin', members: 6 }] }
        if (method === 'talkoot.preview') return { template: { name: 'coding', source: 'builtin', members: 1 }, id: 'rev', home: '/w', budget_usd_per_day: 20, members: [], text: '', digest: 'd' }
        if (method === 'talkoot.create') return { id: 'rev', name: 'rev', home: '/w', members: [] }
        return teamRespond(method)
      },
    })
    const props = { client, hub: new TalkootHub(), onOpenSession: () => {}, onClose: () => {} }
    const view = render(<TalkootScreen {...props} generation={1} />)
    await waitFor(() => expect(lists).toBe(1))
    // A reconnect starts a list read that is still in flight when the team is created.
    view.rerender(<TalkootScreen {...props} generation={2} />)
    await waitFor(() => expect(lists).toBe(2))
    fireEvent.click(await screen.findByText('New team'))
    await waitFor(() => expect(client.sent('talkoot.templates')).toHaveLength(1))
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'rev' } })
    fireEvent.click(screen.getByText('Preview'))
    fireEvent.click(await screen.findByText('Create the team'))
    await waitFor(() => expect(lists).toBe(3))
    // The team opens first. Its read runs in an effect after paint, so a
    // fixed wait here fails on a loaded machine.
    await waitFor(() => expect(client.last('talkoot.get')?.params).toEqual({ id: 'rev' }))
    answerFirst({ talkoots: [] })
    await new Promise((r) => setTimeout(r, 50))
    expect(document.querySelector('.talkoot-team')).toBeTruthy()
  })
})

describe('a shared browser, a changed roster, and a reconnect', () => {
  it('keeps what each person has read apart', async () => {
    const client = teamClient()
    const hub = new TalkootHub()
    const props = { client, hub, id: 'crew', generation: 1, onOpenSession: () => {} }
    const view = render(<TalkootTeam {...props} person="sothr" />)
    await screen.findByText('Hello from the lead')
    // sothr read the lead's message on opening. The room view shows no count.
    await waitFor(() => expect(localStorage.getItem('terva_talkoot_seen:crew:sothr')).toContain('mieli'))
    fireEvent.click(screen.getByText('Room'))
    expect(document.querySelector('.talkoot-unread')).toBeNull()
    // A watcher with no name has read nothing yet.
    view.rerender(<TalkootTeam {...props} person="" />)
    await waitFor(() => expect(document.querySelector('.talkoot-unread')?.textContent).toBe('1'))
  })

  it('commits the name on Enter, not on each key', async () => {
    const client = fakeClient({ respond: (method: Verb) => (method === 'talkoot.list' ? { talkoots: [] } : {}) })
    const view = render(<TalkootScreen client={client} hub={new TalkootHub()} generation={1} onOpenSession={() => {}} onClose={() => {}} />)
    const box = screen.getByLabelText('Your name in the room') as HTMLInputElement
    fireEvent.input(box, { target: { value: 'ai' } })
    expect(localStorage.getItem('terva_talkoot_person')).toBeNull()
    // A re-render while typing keeps the draft.
    view.rerender(<TalkootScreen client={client} hub={new TalkootHub()} generation={1} onOpenSession={() => {}} onClose={() => {}} />)
    expect(box.value).toBe('ai')
    fireEvent.input(box, { target: { value: 'aino' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(localStorage.getItem('terva_talkoot_person')).toBe('aino')
  })

  it('falls back to the coordinator when the open member leaves the roster', async () => {
    let members = crew.members
    const client = fakeClient({
      respond: (method: Verb) => (method === 'talkoot.get' ? { ...crew, members } : teamRespond(method)),
    })
    const hub = new TalkootHub()
    render(<TalkootTeam client={client} hub={hub} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    fireEvent.click(screen.getByText('Planner'))
    await waitFor(() => expect(document.querySelector('.talkoot-main-head strong')!.textContent).toBe('Planner'))
    members = crew.members.filter((m) => m.id !== 'arkkitehti')
    hub.dispatch('#talkoot:crew', { type: 'talkoot_roster', talkoot: { id: 'crew', line: { type: 'roster', at: '2026-09-27T10:02:00Z', by: 'sothr' } } } as WireEvent)
    await waitFor(() => expect(document.querySelector('.talkoot-main-head strong')!.textContent).toBe('Lead'))
    fireEvent.input(screen.getByLabelText('Message to the team'), { target: { value: 'still there?' } })
    fireEvent.submit(document.querySelector('.talkoot-composer')!)
    await waitFor(() => expect(client.last('talkoot.post')!.params).toMatchObject({ to: ['mieli'] }))
  })

  it('drops a roster refresh that a reconnect overtook', async () => {
    const answers: ((v: unknown) => void)[] = []
    let gets = 0
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') {
          gets++
          // The first load answers at once. The refresh hangs until released.
          if (gets === 2) return new Promise((r) => answers.push(r))
          return crew
        }
        return teamRespond(method)
      },
    })
    const hub = new TalkootHub()
    const props = { client, hub, id: 'crew', onOpenSession: () => {}, person: 'sothr' }
    const view = render(<TalkootTeam {...props} generation={1} />)
    await screen.findByText('Hello from the lead')
    hub.dispatch('#talkoot:crew', { type: 'talkoot_roster', talkoot: { id: 'crew', line: { type: 'roster', at: '2026-09-27T10:02:00Z', by: 'sothr' } } } as WireEvent)
    await waitFor(() => expect(gets).toBe(2))
    view.rerender(<TalkootTeam {...props} generation={2} />)
    await waitFor(() => expect(gets).toBe(3))
    // The refresh from the old socket answers last, with a roster the reconnect replaced.
    answers[0]({ ...crew, members: [...crew.members, { id: 'ghost', role: 'specialist', title: 'Ghost', status: { member: 'ghost' } }] })
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByText('Ghost')).toBeNull()
  })
})

describe('a slow answer', () => {
  const roster = { type: 'talkoot_roster', talkoot: { id: 'crew', line: { type: 'roster', at: '2026-09-27T10:02:00Z', by: 'sothr' } } } as WireEvent

  it('drops an older page that a reconnect overtook', async () => {
    let answerOlder: (v: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.room') {
          if ((params as { before?: number }).before) return new Promise((r) => (answerOlder = r))
          const page = teamRespond(method) as { total: number; lines: TalkootLine[] }
          return { ...page, next: 5 }
        }
        return teamRespond(method)
      },
    })
    const props = { client, hub: new TalkootHub(), id: 'crew', onOpenSession: () => {}, person: 'sothr' }
    const view = render(<TalkootTeam {...props} generation={1} />)
    await screen.findByText('Hello from the lead')
    fireEvent.click(screen.getByText('Room'))
    fireEvent.click(screen.getByText('Load earlier messages'))
    await waitFor(() => expect(client.sent('talkoot.room')).toHaveLength(2))
    view.rerender(<TalkootTeam {...props} generation={2} />)
    await waitFor(() => expect(client.sent('talkoot.room')).toHaveLength(3))
    answerOlder({ total: 6, lines: [{ type: 'envelope', at: '2026-09-27T09:00:00Z', envelope: { id: 'old', talkoot: 'crew', from: 'mieli', to: ['human:sothr'], kind: 'message', body: 'From the old socket', chain: { root: 'old', hops: 0 }, at: '2026-09-27T09:00:00Z' } }] })
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByText('From the old socket')).toBeNull()
  })

  it('retries a roster refresh that failed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      let gets = 0
      const client = fakeClient({
        respond: (method: Verb) => {
          if (method === 'talkoot.get') {
            gets++
            if (gets === 2) throw new Error('the daemon is busy')
          }
          return teamRespond(method)
        },
      })
      const hub = new TalkootHub()
      render(<TalkootTeam client={client} hub={hub} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
      await screen.findByText('Hello from the lead')
      hub.dispatch('#talkoot:crew', roster)
      await screen.findByText('the daemon is busy')
      vi.advanceTimersByTime(REFRESH_RETRY_MS)
      await waitFor(() => expect(gets).toBe(3))
      // The retry succeeded, so its error goes.
      await waitFor(() => expect(screen.queryByText('the daemon is busy')).toBeNull())
    } finally {
      vi.useRealTimers()
    }
  })

  it('retries a load that failed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      let gets = 0
      const client = fakeClient({
        respond: (method: Verb) => {
          if (method === 'talkoot.get' && ++gets === 1) throw new Error('not yet')
          return teamRespond(method)
        },
      })
      render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
      await screen.findByText('not yet')
      vi.advanceTimersByTime(REFRESH_RETRY_MS)
      await screen.findByText('Hello from the lead')
      expect(screen.queryByText('not yet')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('waits longer after each failed load, up to a limit', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const client = fakeClient({
        respond: (method: Verb) => {
          if (method === 'talkoot.get') throw new Error('stopped')
          return teamRespond(method)
        },
      })
      render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
      const gets = () => client.sent('talkoot.get').length
      await waitFor(() => expect(gets()).toBe(1))
      // 5 s, then 10 s: the second wait is longer than the first.
      vi.advanceTimersByTime(REFRESH_RETRY_MS)
      await waitFor(() => expect(gets()).toBe(2))
      vi.advanceTimersByTime(REFRESH_RETRY_MS)
      await new Promise((r) => setTimeout(r, 10))
      expect(gets()).toBe(2)
      vi.advanceTimersByTime(REFRESH_RETRY_MS)
      await waitFor(() => expect(gets()).toBe(3))
      // Well past the limit, it still tries once per LOAD_RETRY_MAX_MS.
      for (let i = 0; i < 6; i++) {
        vi.advanceTimersByTime(LOAD_RETRY_MAX_MS)
        await new Promise((r) => setTimeout(r, 5))
      }
      const n = gets()
      vi.advanceTimersByTime(LOAD_RETRY_MAX_MS)
      await waitFor(() => expect(gets()).toBe(n + 1))
    } finally {
      vi.useRealTimers()
    }
  })

  it('drops the error of an older page that a reconnect overtook', async () => {
    let failOlder: (e: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.room') {
          if ((params as { before?: number }).before) return new Promise((_, reject) => (failOlder = reject))
          return { ...(teamRespond(method) as object), next: 5 }
        }
        return teamRespond(method)
      },
    })
    const props = { client, hub: new TalkootHub(), id: 'crew', onOpenSession: () => {}, person: 'sothr' }
    const view = render(<TalkootTeam {...props} generation={1} />)
    await screen.findByText('Hello from the lead')
    fireEvent.click(screen.getByText('Room'))
    fireEvent.click(screen.getByText('Load earlier messages'))
    await waitFor(() => expect(client.sent('talkoot.room')).toHaveLength(2))
    view.rerender(<TalkootTeam {...props} generation={2} />)
    await waitFor(() => expect(client.sent('talkoot.room')).toHaveLength(3))
    failOlder(new Error('the old socket closed'))
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByText('the old socket closed')).toBeNull()
  })

  it('keeps a new draft typed while a post is in flight', async () => {
    let answerPost: (v: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb) => (method === 'talkoot.post' ? new Promise((r) => (answerPost = r)) : teamRespond(method)),
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    const box = screen.getByLabelText('Message to the team') as HTMLTextAreaElement
    fireEvent.input(box, { target: { value: 'first' } })
    fireEvent.submit(document.querySelector('.talkoot-composer')!)
    await waitFor(() => expect(client.sent('talkoot.post')).toHaveLength(1))
    fireEvent.input(box, { target: { value: 'second thought' } })
    answerPost({})
    await new Promise((r) => setTimeout(r, 20))
    expect(box.value).toBe('second thought')
  })
})

describe('an answer line', () => {
  it('names the member it answered when the room records no person', () => {
    render(<RoomLine line={{ type: 'answer', at: '2026-09-27T10:00:00Z', member: 'arkkitehti', answers: [] }} />)
    expect(document.querySelector('.talkoot-line.system')!.textContent).toMatch(/^The answer to arkkitehti:/)
  })
})

describe('a member at work, and the files it cites', () => {
  it('names the tool a working member runs', async () => {
    const client = teamClient()
    const hub = new TalkootHub()
    render(<TalkootTeam client={client} hub={hub} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    hub.dispatch('#talkoot:crew', { type: 'talkoot_status', talkoot: { id: 'crew', members: [{ member: 'arkkitehti', working: true, tool: 'grep' }] } } as WireEvent)
    await screen.findByText('running grep')
    hub.dispatch('#talkoot:crew', { type: 'talkoot_status', talkoot: { id: 'crew', members: [{ member: 'arkkitehti', working: false }] } } as WireEvent)
    await waitFor(() => expect(screen.queryByText('running grep')).toBeNull())
  })

  it('opens a path: reference in place, and leaves a ticket: reference a label', async () => {
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.ref') return { ref: (params as { ref: string }).ref, text: '# The plan', size: 10 }
        if (method === 'talkoot.room')
          return {
            total: 1,
            lines: [
              {
                type: 'envelope',
                at: '2026-09-27T10:00:00Z',
                envelope: { id: 'e1', talkoot: 'crew', from: 'mieli', to: ['human:sothr'], kind: 'message', body: 'See the plan', refs: ['path:docs/plan.md', 'ticket:TKT-1'], chain: { root: 'e1', hops: 0 }, at: '2026-09-27T10:00:00Z' },
              },
            ],
          }
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('See the plan')
    expect(screen.getByText('ticket:TKT-1').closest('button')).toBeNull()
    fireEvent.click(screen.getByText('path:docs/plan.md'))
    await screen.findByText('# The plan')
    expect(client.last('talkoot.ref')!.params).toEqual({ id: 'crew', ref: 'path:docs/plan.md' })
    // Closing and opening again reads nothing twice.
    fireEvent.click(screen.getByText('path:docs/plan.md'))
    fireEvent.click(screen.getByText('path:docs/plan.md'))
    expect(client.sent('talkoot.ref')).toHaveLength(1)
  })

  it('says why a reference did not open', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.ref') throw new Error('talkoot: path:gone.md does not exist in the home checkout')
        if (method === 'talkoot.room')
          return {
            total: 1,
            lines: [{ type: 'envelope', at: '2026-09-27T10:00:00Z', envelope: { id: 'e1', talkoot: 'crew', from: 'mieli', to: ['human:sothr'], kind: 'message', body: 'Gone', refs: ['path:gone.md'], chain: { root: 'e1', hops: 0 }, at: '2026-09-27T10:00:00Z' } }],
          }
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    fireEvent.click(await screen.findByText('path:gone.md'))
    await screen.findByText(/does not exist in the home checkout/)
  })
})

describe('a long reference', () => {
  it('counts the bytes it shows, not the characters', async () => {
    const text = '€'.repeat(4)
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.ref') return { ref: 'path:big.txt', text, size: 100, truncated: true }
        if (method === 'talkoot.room')
          return {
            total: 1,
            lines: [{ type: 'envelope', at: '2026-09-27T10:00:00Z', envelope: { id: 'e1', talkoot: 'crew', from: 'mieli', to: ['human:sothr'], kind: 'message', body: 'Big', refs: ['path:big.txt'], chain: { root: 'e1', hops: 0 }, at: '2026-09-27T10:00:00Z' } }],
          }
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    fireEvent.click(await screen.findByText('path:big.txt'))
    await screen.findByText('Showing the first 12 of 100 bytes.')
  })
})

describe('member marks', () => {
  const marked: TalkootView = {
    ...crew,
    members: [
      { ...crew.members[0], mark: { shape: 'hexagon', color: '#3E63DD' } },
      { ...crew.members[1], mark: { shape: 'shield', color: '#FFB224' } },
    ],
  }

  it('draws a member mark in the sidebar, beside its posts, and on its cards', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return marked
        if (method === 'talkoot.inbox') return { cards: [{ session: 's-lead', member: 'mieli', kind: 'ask', id: 'q1' }] }
        return teamRespond(method)
      },
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    const side = [...document.querySelectorAll('.talkoot-member-row .talkoot-mark')].map((e) => e.getAttribute('data-shape'))
    expect(side).toEqual(['shield', 'hexagon'])
    const post = document.querySelector('.talkoot-line.envelope .talkoot-mark')
    expect(post?.getAttribute('data-shape')).toBe('shield')
    expect(post?.querySelector('g')?.getAttribute('fill')).toBe('#FFB224')
    expect(document.querySelector('.talkoot-card-from .talkoot-mark')?.getAttribute('data-shape')).toBe('shield')
  })

  it('draws nothing for a member of a daemon from before marks', async () => {
    render(<TalkootTeam client={teamClient()} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    expect(document.querySelector('.talkoot-mark')).toBeNull()
  })

  it('draws every shape in the set with a body of its own', () => {
    expect([...drawnShapes()].sort()).toEqual([...MARK_SHAPES].sort())
  })

  it('picks eyes that read on the fill', () => {
    expect(eyeColor('#FFB224')).toBe('#1a1a1a')
    expect(eyeColor('#3E63DD')).toBe('#ffffff')
    expect(eyeColor('not a colour')).toBe('#ffffff')
  })
})

describe('the member card', () => {
  const lead = { ...crew.members[1], posture: 'plan', mark: { shape: 'shield', color: '#FFB224' } }
  const cardView: TalkootView = { ...crew, members: [crew.members[0], lead] }
  const cardClient = (view: TalkootView = cardView) =>
    fakeClient({ respond: (method: Verb) => (method === 'talkoot.get' ? view : teamRespond(method)) })
  const open = async (client: ReturnType<typeof fakeClient>, person = 'sothr') => {
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person={person} onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    fireEvent.click(screen.getByText('Member card'))
    return screen.getByLabelText('Member card')
  }

  it('saves only the fields the person changed, at once', async () => {
    const client = cardClient()
    await open(client)
    fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Chief' } })
    fireEvent.change(screen.getByLabelText('Posture'), { target: { value: 'ask' } })
    fireEvent.input(screen.getByLabelText('Budget per day, USD'), { target: { value: '2.5' } })
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.last('talkoot.update')).toBeTruthy())
    expect(client.last('talkoot.update')!.params).toEqual({
      id: 'crew',
      by: 'sothr',
      ops: [{ op: 'edit', member: 'mieli', set: { title: 'Chief', posture: 'ask', budget_usd_per_day: 2.5 } }],
    })
  })

  it('sends a number that does not parse as its text, so the daemon refuses it', async () => {
    const client = cardClient()
    await open(client)
    fireEvent.input(screen.getByLabelText('Turns per day'), { target: { value: 'lots' } })
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.last('talkoot.update')).toBeTruthy())
    expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { turns_per_day: 'lots' } }] })
  })

  it('sets a shape and a colour from the pickers, and resets the mark', async () => {
    const client = cardClient({ ...cardView, members: [crew.members[0], { ...lead, own_mark: { shape: 'tab' } }] })
    await open(client)
    expect(screen.getAllByRole('button', { name: /^#/ })).toHaveLength(13)
    fireEvent.click(screen.getByRole('button', { name: '#3E63DD' }))
    await waitFor(() => expect(client.last('talkoot.update')).toBeTruthy())
    expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ op: 'look', member: 'mieli', set: { mark: { shape: 'tab', color: '#3E63DD' } } }] })
    fireEvent.click(screen.getByRole('button', { name: 'hexagon' }))
    await waitFor(() => expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { mark: { shape: 'hexagon' } } }] }))
    fireEvent.click(screen.getByText('Reset the mark to its default'))
    await waitFor(() => expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ op: 'look', set: { mark: null } }] }))
  })

  it('keeps what the person types when another edit reaches the member', async () => {
    let view = cardView
    const client = fakeClient({ respond: (method: Verb) => (method === 'talkoot.get' ? view : teamRespond(method)) })
    const hub = new TalkootHub()
    render(<TalkootTeam client={client} hub={hub} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('Hello from the lead')
    fireEvent.click(screen.getByText('Member card'))
    fireEvent.input(screen.getByLabelText('Budget per day, USD'), { target: { value: '3' } })
    view = { ...cardView, members: [crew.members[0], { ...lead, title: 'Chief' }] }
    hub.dispatch('#talkoot:crew', { type: 'talkoot_roster', talkoot: { id: 'crew', line: { type: 'roster', at: '2026-09-27T10:02:00Z', by: 'ana' } } } as WireEvent)
    await waitFor(() => expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Chief'))
    // ⚠️ Preact runs effects after paint, so a reset would land after the
    // title shows. Let them run before reading the draft.
    await new Promise((r) => setTimeout(r, 50))
    expect((screen.getByLabelText('Budget per day, USD') as HTMLInputElement).value).toBe('3')
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { budget_usd_per_day: 3 } }] }))
    expect(Object.keys((client.last('talkoot.update')!.params as { ops: { set: object }[] }).ops[0].set)).toEqual(['budget_usd_per_day'])
  })

  it('returns the posture to its default', async () => {
    const client = cardClient()
    await open(client)
    fireEvent.change(screen.getByLabelText('Posture'), { target: { value: '' } })
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { posture: null } }] }))
  })

  it('merges a second pick into the first, and sends nothing for the mark it has', async () => {
    const client = cardClient({ ...cardView, members: [crew.members[0], { ...lead, own_mark: { shape: 'tab' } }] })
    await open(client)
    fireEvent.click(screen.getByRole('button', { name: 'tab' }))
    expect(client.last('talkoot.update')).toBeUndefined()
    // The view has not caught up with the colour when the shape is picked.
    fireEvent.click(screen.getByRole('button', { name: '#3E63DD' }))
    fireEvent.click(screen.getByRole('button', { name: 'hexagon' }))
    await waitFor(() =>
      expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { mark: { shape: 'hexagon', color: '#3E63DD' } } }] }),
    )
  })

  it('sends one pick after another, and starts clean on another member', async () => {
    const answers: ((v: unknown) => void)[] = []
    const view = { ...cardView, members: [{ ...crew.members[0], mark: { shape: 'pill', color: '#8D8D8D' } }, { ...lead, own_mark: { shape: 'tab' } }] }
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return view
        if (method === 'talkoot.update') return new Promise((r) => answers.push(r))
        return teamRespond(method)
      },
    })
    await open(client)
    fireEvent.click(screen.getByRole('button', { name: '#3E63DD' }))
    fireEvent.click(screen.getByRole('button', { name: 'hexagon' }))
    await waitFor(() => expect(client.sent('talkoot.update')).toHaveLength(1))
    // The second pick waits for the first answer, so it cannot apply first.
    await new Promise((r) => setTimeout(r, 20))
    expect(client.sent('talkoot.update')).toHaveLength(1)
    answers[0]({})
    await waitFor(() => expect(client.sent('talkoot.update')).toHaveLength(2))
    expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ member: 'mieli', set: { mark: { shape: 'hexagon', color: '#3E63DD' } } }] })

    // Another member's card owes nothing to the pick still on its way.
    fireEvent.click(screen.getByText('Planner'))
    await waitFor(() => expect(document.querySelector('.talkoot-main-head strong')!.textContent).toBe('Planner'))
    fireEvent.click(screen.getByRole('button', { name: '#46A758' }))
    await waitFor(() => expect(client.sent('talkoot.update')).toHaveLength(3))
    expect(client.last('talkoot.update')!.params).toEqual({ id: 'crew', by: 'sothr', ops: [{ op: 'look', member: 'arkkitehti', set: { mark: { color: '#46A758' } } }] })
  })

  it('falls back to the roster mark after a refused pick', async () => {
    let refuse = true
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return { ...cardView, members: [crew.members[0], { ...lead, own_mark: { shape: 'tab' } }] }
        if (method === 'talkoot.update' && refuse) throw new Error('refused')
        return teamRespond(method)
      },
    })
    await open(client)
    fireEvent.click(screen.getByRole('button', { name: '#3E63DD' }))
    fireEvent.click(screen.getByRole('button', { name: 'hexagon' }))
    await waitFor(() => expect(client.sent('talkoot.update')).toHaveLength(2))
    await screen.findByText('refused')
    refuse = false
    // The refused colour is not a base for the next pick.
    fireEvent.click(screen.getByRole('button', { name: 'drop' }))
    await waitFor(() => expect(client.sent('talkoot.update')).toHaveLength(3))
    expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { mark: { shape: 'drop' } } }] })
    expect(client.last('talkoot.update')!.params.ops![0].set!.mark).toEqual({ shape: 'drop' })
  })

  it('waits for a save before it offers Save again', async () => {
    let done: (v: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return cardView
        if (method === 'talkoot.update') return new Promise((r) => (done = r))
        return teamRespond(method)
      },
    })
    await open(client)
    fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Chief' } })
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect((screen.getByText('Save changes') as HTMLButtonElement).disabled).toBe(true))
    fireEvent.click(screen.getByText('Save changes'))
    expect(client.sent('talkoot.update')).toHaveLength(1)
    done({})
  })

  it('counts a tools list by its names, not its spacing', async () => {
    await open(cardClient({ ...cardView, members: [crew.members[0], { ...lead, tools: ['read', 'grep'] }] }))
    fireEvent.input(screen.getByLabelText('Tools'), { target: { value: 'read,grep' } })
    expect((screen.getByText('Save changes') as HTMLButtonElement).disabled).toBe(true)
  })

  it('keeps a box typed in while a save was on its way', async () => {
    let done: (v: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return cardView
        if (method === 'talkoot.update') return new Promise((r) => (done = r))
        return teamRespond(method)
      },
    })
    await open(client)
    fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Chief' } })
    fireEvent.click(screen.getByText('Save changes'))
    fireEvent.input(screen.getByLabelText('Model'), { target: { value: 'opus' } })
    done({})
    await waitFor(() => expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Lead'))
    expect((screen.getByLabelText('Model') as HTMLInputElement).value).toBe('opus')
  })

  it('offers no reset for a member on its default mark', async () => {
    await open(cardClient())
    expect(screen.queryByText('Reset the mark to its default')).toBeNull()
  })

  it('asks twice before it removes a member', async () => {
    const client = cardClient()
    await open(client)
    fireEvent.click(screen.getByText('Remove'))
    expect(client.last('talkoot.update')).toBeUndefined()
    fireEvent.click(screen.getByText('Remove mieli from the team'))
    await waitFor(() => expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ op: 'remove', member: 'mieli' }] }))
  })

  it('disarms a refused remove, and cancels an armed one', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return cardView
        if (method === 'talkoot.update') throw new Error('talkoot: a talkoot needs a coordinator')
        return teamRespond(method)
      },
    })
    await open(client)
    fireEvent.click(screen.getByText('Remove'))
    fireEvent.click(screen.getByText('Cancel'))
    expect(screen.queryByText('Remove mieli from the team')).toBeNull()
    fireEvent.click(screen.getByText('Remove'))
    fireEvent.click(screen.getByText('Remove mieli from the team'))
    await screen.findByText('talkoot: a talkoot needs a coordinator')
    expect(screen.queryByText('Remove mieli from the team')).toBeNull()
    expect(client.sent('talkoot.update')).toHaveLength(1)
  })

  it('shows the fields but edits nothing until the person has a name', async () => {
    await open(cardClient(), '')
    expect((screen.getByLabelText('Title') as HTMLInputElement).disabled).toBe(true)
    expect(screen.queryByText('Save changes')).toBeNull()
  })

  it("shows the persona's charter and the work it suits", async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return { ...cardView, members: [crew.members[0], { ...lead, persona: 'vartija' }] }
        if (method === 'personas.get') return { name: 'Vartija', charter: 'Guard the gate.', good_for: ['review'], avoid_for: ['speed'] }
        return teamRespond(method)
      },
    })
    await open(client)
    await screen.findByText('Good for: review')
    expect(screen.getByText('Avoid for: speed')).toBeTruthy()
    expect(screen.getByText('Guard the gate.')).toBeTruthy()
    expect(client.last('personas.get')!.params).toEqual({ ref: 'vartija' })
  })
})

describe('the proposal card', () => {
  const proposalClient = (proposal: object) =>
    fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.get') return { ...crew, members: [crew.members[0], { ...crew.members[1], mark: { shape: 'shield', color: '#FFB224' } }] }
        if (method === 'talkoot.inbox') return { cards: [{ session: '', member: 'mieli', kind: 'proposal', id: 'p1', proposal }] }
        return teamRespond(method)
      },
    })
  const base = { id: 'p1', proposer: 'mieli', at: '2026-09-27T10:00:00Z', title: 'mieli proposes', summary: 'a change', ops: [], status: 'pending' }

  it('marks each authority field, and flags the ones that grant more', async () => {
    const client = proposalClient({
      ...base,
      class: 'authority',
      changes: [
        {
          member: 'arkkitehti',
          before: { id: 'arkkitehti', role: 'planner', posture: 'plan', title: 'Planner' },
          after: { id: 'arkkitehti', role: 'planner', posture: 'auto-edit', title: 'Planner' },
          widens: ['posture'],
          authority: ['posture'],
        },
        { member: 'scout', after: { id: 'scout', role: 'specialist', tools: ['read', 'grep'] }, authority: ['tools'] },
      ],
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    const row = (await screen.findByText('auto-edit (grants more)')).closest('tr')!
    expect(row.className).toContain('authority')
    expect(row.className).toContain('widens')
    expect(row.textContent).toContain('plan')
    // An unchanged field of a changed member stays out; a joining member shows whole.
    const fields = [...document.querySelectorAll('.talkoot-fields th')].map((e) => e.firstChild?.textContent)
    expect(fields).toEqual(['posture', 'role', 'tools'])
    expect(screen.getByText('read, grep')).toBeTruthy()
  })

  it('shows a look batch as marks before and after, side by side', async () => {
    const client = proposalClient({
      ...base,
      class: 'look',
      changes: [
        {
          member: 'mieli',
          before: { id: 'mieli', role: 'coordinator' },
          after: { id: 'mieli', role: 'coordinator', mark: { shape: 'cloud', color: '#46A758' } },
        },
      ],
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    const before = await screen.findByRole('img', { name: 'mieli before' })
    // A mark the roster does not set draws as the member's mark today.
    expect(before.getAttribute('data-shape')).toBe('shield')
    expect(screen.getByRole('img', { name: 'mieli after' }).getAttribute('data-shape')).toBe('cloud')
  })

  it('draws the default a reset returns to, from the marks the daemon resolved', async () => {
    const client = proposalClient({
      ...base,
      class: 'look',
      changes: [
        {
          member: 'mieli',
          before: { id: 'mieli', role: 'coordinator', mark: { shape: 'shield', color: '#FFB224' } },
          after: { id: 'mieli', role: 'coordinator' },
          mark_before: { shape: 'shield', color: '#FFB224' },
          mark_after: { shape: 'drop', color: '#12A594' },
        },
      ],
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    const after = await screen.findByRole('img', { name: 'mieli after' })
    expect(after.getAttribute('data-shape')).toBe('drop')
    expect(screen.getByRole('img', { name: 'mieli before' }).getAttribute('data-shape')).toBe('shield')
  })

  // The daemon side, the answer line in the room, is TKT-01M3AGQ85Y's test.
  it('shows an ask card the warning that the room keeps the answer', async () => {
    const client = fakeClient({
      respond: (method: Verb) =>
        method === 'talkoot.inbox'
          ? { cards: [{ session: 's-lead', member: 'mieli', kind: 'ask', id: 'q1', ask: { id: 'q1', questions: [{ question: 'Which schema?' }] } }] }
          : teamRespond(method),
    })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => {}} />)
    await screen.findByText('This answer is kept in the talkoot room, and a teammate may see it.')
  })
})
