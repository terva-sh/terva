// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { afterEach, describe, expect, it } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { TalkootPreview, Verb } from '../../platform/ctrlproto/types'
import { charterOf, composeRoster, draftFromPreview } from './builder'
import { NewTalkoot } from './NewTalkoot'

afterEach(cleanup)

describe('composeRoster', () => {
  it('writes a roster with no home, quoted values, and the charter', () => {
    const text = composeRoster({
      name: 'duo',
      title: 'Duo: a pair',
      budget: '3',
      members: [
        { id: 'lead', role: 'coordinator', persona: 'mieli', posture: 'auto-edit' },
        { id: 'helper', role: 'specialist', reviewer: true, tools: ['read', 'grep'], mark: { shape: 'circle' }, turns_per_day: 10 },
      ],
      charter: 'Work in pairs.\n',
    })
    expect(text).not.toContain('home:')
    expect(text).toContain('title: "Duo: a pair"')
    expect(text).toContain('budget_usd_per_day: 3')
    expect(text).toContain('  - id: "lead"\n    role: "coordinator"\n    persona: "mieli"\n    posture: "auto-edit"\n')
    expect(text).toContain('    reviewer: true\n')
    expect(text).toContain('    tools: ["read", "grep"]\n')
    expect(text).toContain('    mark: {shape: "circle"}\n')
    expect(charterOf(text)).toBe('Work in pairs.')
  })

  it('cannot open a new key from a value', () => {
    const text = composeRoster({ name: 'x', title: '', budget: '1', members: [{ id: 'a', role: 'coordinator', persona: 'mieli\nhome: /etc' }], charter: '' })
    expect(text).not.toMatch(/^home:/m)
    expect(text).toContain('persona: "mieli\\nhome: /etc"')
  })

  it('sends a budget that does not parse as text, so the daemon refuses it', () => {
    expect(composeRoster({ name: 'x', title: '', budget: 'lots', members: [], charter: '' })).toContain('budget_usd_per_day: "lots"')
  })

  it('starts from a template preview with its members, budget, and charter', () => {
    const pv: TalkootPreview = {
      template: { name: 'review-pair', title: 'Review pair', source: 'builtin', members: 1 },
      id: 'draft', home: '/w', budget_usd_per_day: 8, digest: 'd',
      members: [{ id: 'developer', role: 'coordinator', driver: 'native', posture: 'auto-edit', workspace: 'shared', available: true }],
      text: '---\nname: review-pair\n---\n\nReview everything.\n',
    }
    const d = draftFromPreview(pv, 'mine')
    expect(d).toMatchObject({ title: 'Review pair', budget: '8', charter: 'Review everything.' })
    expect(d.members[0]).toEqual({ id: 'developer', role: 'coordinator', driver: 'native', posture: 'auto-edit', workspace: 'shared' })
  })
})

describe('the team builder', () => {
  it('previews the composed roster, shows its problems, and creates from the preview text', async () => {
    let problems = ['members lead, helper can all write to the shared checkout']
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.templates') return { templates: [] }
        if (method === 'personas.list') return { personas: [{ name: 'Mieli', ref: 'mieli' }] }
        if (method === 'talkoot.preview') {
          const p = params as { text: string; id: string }
          return { template: { name: 'composed', source: 'composed', members: 2 }, id: p.id, home: '/w', budget_usd_per_day: 5, members: [], text: p.text + '# checked\n', problems, digest: 'd' }
        }
        if (method === 'talkoot.create') return { id: 'duo', name: 'duo', home: '/w', members: [] }
        return {}
      },
    })
    let created = ''
    render(<NewTalkoot client={client} taken={[]} onCreated={(id) => (created = id)} onCancel={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Build a team' }))
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'duo' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add a member' }))
    fireEvent.input(screen.getByLabelText('member-2 Id'), { target: { value: 'helper' } })
    fireEvent.input(screen.getByPlaceholderText(/What every member reads/), { target: { value: 'Work in pairs.' } })

    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))
    await screen.findByText('members lead, helper can all write to the shared checkout')
    expect((screen.getByRole('button', { name: 'Create the team' }) as HTMLButtonElement).disabled).toBe(true)
    const sent = client.last('talkoot.preview')!.params as { text: string; id: string; template?: string }
    expect(sent.id).toBe('duo')
    expect(sent.template).toBeUndefined()
    expect(sent.text).toContain('name: "duo"')
    expect(sent.text).toContain('  - id: "helper"\n    role: "specialist"\n')
    expect(sent.text).toContain('Work in pairs.')

    problems = []
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Create the team' }) as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(screen.getByRole('button', { name: 'Create the team' }))
    await waitFor(() => expect(created).toBe('duo'))
    // Create sends the text the daemon returned, not a fresh compose.
    expect((client.last('talkoot.create')!.params as { text: string }).text).toContain('# checked')
  })

  it('keeps a half-typed tool list and budget in the box', async () => {
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.templates') return { templates: [] }
        if (method === 'talkoot.preview') return { template: { name: 'composed', source: 'composed', members: 1 }, id: 'duo', home: '/w', budget_usd_per_day: 5, members: [], text: (params as { text: string }).text, digest: 'd' }
        return {}
      },
    })
    render(<NewTalkoot client={client} taken={[]} onCreated={() => {}} onCancel={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Build a team' }))
    const title = screen.getByLabelText('lead Title') as HTMLInputElement
    fireEvent.input(title, { target: { value: 'Lead ' } })
    expect(title.value).toBe('Lead ')
    fireEvent.input(title, { target: { value: 'Lead dev ' } })
    const tools = screen.getByLabelText('lead Tools') as HTMLInputElement
    fireEvent.input(tools, { target: { value: 'read, ' } })
    expect(tools.value).toBe('read, ')
    fireEvent.input(tools, { target: { value: 'read, grep' } })
    expect(tools.value).toBe('read, grep')
    const budget = screen.getByLabelText('lead Budget per day, USD') as HTMLInputElement
    fireEvent.input(budget, { target: { value: '1.' } })
    expect(budget.value).toBe('1.')
    fireEvent.input(budget, { target: { value: '1.5' } })
    expect(budget.value).toBe('1.5')
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'duo' } })
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))
    await waitFor(() => expect(client.sent('talkoot.preview')).toHaveLength(1))
    const text = (client.last('talkoot.preview')!.params as { text: string }).text
    expect(text).toContain('    title: "Lead dev"\n')
    expect(text).toContain('    budget_usd_per_day: 1.5\n')
    expect(text).toContain('    tools: ["read", "grep"]\n')
  })

  it('an edit while a template loads keeps the select on the start the draft came from', async () => {
    let answer: (v: unknown) => void = () => {}
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'talkoot.templates') return { templates: [{ name: 'review-pair', title: 'Review pair', source: 'builtin', members: 2 }] }
        if (method === 'talkoot.preview') return new Promise((r) => (answer = r))
        return {}
      },
    })
    render(<NewTalkoot client={client} taken={[]} onCreated={() => {}} onCancel={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Build a team' }))
    const start = (await screen.findByDisplayValue('an empty team')) as HTMLSelectElement
    await screen.findByText('Review pair')
    fireEvent.change(start, { target: { value: 'review-pair' } })
    fireEvent.input(screen.getByLabelText('lead Title'), { target: { value: 'Lead' } })
    answer({ template: { name: 'review-pair', source: 'builtin', members: 1 }, id: 'draft', home: '/w', budget_usd_per_day: 8, members: [{ id: 'developer', role: 'coordinator', driver: 'native', posture: 'auto-edit', workspace: 'shared', available: true }], text: '---\n---\n', digest: 'd' })
    await new Promise((r) => setTimeout(r, 20))
    expect(start.value).toBe('')
    expect(screen.getByLabelText('lead Title')).toBeTruthy()
  })

  it('an edit after the preview clears it', async () => {
    const client = fakeClient({
      respond: (method: Verb, params: unknown) => {
        if (method === 'talkoot.templates') return { templates: [] }
        if (method === 'talkoot.preview') return { template: { name: 'composed', source: 'composed', members: 1 }, id: 'duo', home: '/w', budget_usd_per_day: 5, members: [], text: (params as { text: string }).text, digest: 'd' }
        return {}
      },
    })
    render(<NewTalkoot client={client} taken={[]} onCreated={() => {}} onCancel={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Build a team' }))
    fireEvent.input(screen.getByPlaceholderText('team'), { target: { value: 'duo' } })
    fireEvent.click(screen.getByRole('button', { name: 'Preview' }))
    await screen.findByRole('button', { name: 'Create the team' })
    fireEvent.input(screen.getByLabelText('lead Title'), { target: { value: 'Lead' } })
    expect(screen.queryByRole('button', { name: 'Create the team' })).toBeNull()
  })
})
