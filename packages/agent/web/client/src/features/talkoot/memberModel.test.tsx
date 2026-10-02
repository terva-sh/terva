// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { TalkootMember, TalkootView } from '../../platform/ctrlproto/types'
import { TalkootHub } from '../../platform/talkoot/hub'
import { MemberCard } from './MemberCard'
import { TalkootTeam } from './TalkootTeam'
import { memberModelSummary } from './memberModel'

const member: TalkootMember = { id: 'lead', role: 'coordinator', driver: 'native', tier: 'strong', resolved_provider: 'anthropic', resolved_model: 'resolved-opus', model_source: 'tier', status: { member: 'lead' } }
const models = [
  { id: 'catalog-opus', provider: 'anthropic' },
  { id: 'catalog-flash', provider: 'google' },
  { id: 'hidden-model', provider: 'google', hidden: true },
]
const clientFor = () => fakeClient({ respond: (method) => method === 'models.list' ? { models } : {} })
function card(m = member, client = clientFor(), canSteer = true) {
  render(<MemberCard client={client} id="crew" member={m} person="sothr" canSteer={canSteer} onPause={() => {}} onResume={() => {}} />)
  return client
}
afterEach(cleanup)

describe('talkoot member models', () => {
  it('offers searchable catalog IDs without hidden models or a session request', async () => {
    const client = card()
    const select = screen.getByLabelText('Model') as HTMLSelectElement
    await waitFor(() => expect(select.textContent).toContain('catalog-opus'))
    expect(select.textContent).not.toContain('hidden-model')
    fireEvent.input(screen.getByLabelText('Search models'), { target: { value: 'FLASH' } })
    expect(select.textContent).toContain('catalog-flash')
    expect(select.textContent).not.toContain('catalog-opus')
    expect(client.commands.map((c) => c.method)).toEqual(['models.list'])
    expect(client.last('models.list')!.sess).toBe('')
  })

  it('chooses a catalog model and clears the tier in one edit', async () => {
    const client = card()
    await screen.findByRole('option', { name: 'catalog-opus' })
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'catalog-opus' } })
    expect((screen.getByLabelText('Tier') as HTMLSelectElement).value).toBe('')
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.last('talkoot.update')).toBeTruthy())
    expect(client.last('talkoot.update')!.params).toEqual({ id: 'crew', by: 'sothr', ops: [{ op: 'edit', member: 'lead', set: { model: 'catalog-opus', tier: null } }] })
  })

  it('sends the tier clear even when the old snapshot had no tier', async () => {
    const client = card({ ...member, tier: undefined })
    await screen.findByRole('option', { name: 'catalog-opus' })
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'catalog-opus' } })
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.last('talkoot.update')).toBeTruthy())
    expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { model: 'catalog-opus', tier: null } }] })
  })

  it('chooses a tier and clears an explicit model in one edit', async () => {
    const client = card({ ...member, tier: undefined, model: 'catalog-opus' })
    const tier = screen.getByLabelText('Tier') as HTMLSelectElement
    expect([...tier.options].map((o) => o.value)).toEqual(['', 'weak', 'medium', 'strong', 'cheap'])
    fireEvent.change(tier, { target: { value: 'cheap' } })
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.last('talkoot.update')).toBeTruthy())
    expect(client.last('talkoot.update')!.params).toMatchObject({ ops: [{ set: { model: null, tier: 'cheap' } }] })
  })

  it('keeps custom values available when the catalog is unavailable', async () => {
    const client = fakeClient({ respond: () => Promise.reject(new Error('catalog offline')) })
    card(member, client)
    await screen.findByText('Could not load models: catalog offline')
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: '#custom' } })
    fireEvent.input(screen.getByLabelText('Custom model'), { target: { value: 'local-custom' } })
    expect((screen.getByLabelText('Tier') as HTMLSelectElement).value).toBe('')
    expect((screen.getByLabelText('Custom model') as HTMLInputElement).value).toBe('local-custom')
  })

  it('keeps a custom model typed while another field saves', async () => {
    let done!: (v: unknown) => void
    const client = fakeClient({ respond: (method) => method === 'talkoot.update' ? new Promise((resolve) => { done = resolve }) : { models } })
    card(member, client)
    fireEvent.input(screen.getByLabelText('Title'), { target: { value: 'Chief' } })
    fireEvent.click(screen.getByText('Save changes'))
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: '#custom' } })
    fireEvent.input(screen.getByLabelText('Custom model'), { target: { value: 'custom-opus' } })
    done({})
    await waitFor(() => expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe(''))
    expect((screen.getByLabelText('Custom model') as HTMLInputElement).value).toBe('custom-opus')
  })

  it('preserves a hidden current model as a choice', async () => {
    card({ ...member, model: 'hidden-model', tier: undefined })
    await waitFor(() => expect(screen.getByRole('option', { name: 'catalog-opus' })).toBeTruthy())
    expect((screen.getByLabelText('Model') as HTMLSelectElement).value).toBe('hidden-model')
  })

  it('does not offer the native catalog to Claude or claim its requested model is live', () => {
    const m = { ...member, driver: 'claude', model: 'claude-opus-4-8', tier: undefined, resolved_provider: undefined, resolved_model: undefined }
    const client = card(m)
    expect(client.sent('models.list')).toHaveLength(0)
    expect(screen.queryByLabelText('Search models')).toBeNull()
    expect((screen.getByLabelText('Model') as HTMLSelectElement).textContent).not.toContain('catalog-flash')
    expect(memberModelSummary(m)).toBe('claude: requested model claude-opus-4-8')
    expect(memberModelSummary({ ...m, model: undefined })).toBe('claude: backend default')
  })

  it('shows resolved models for every member without opening the member card or a session', async () => {
    const view: TalkootView = { id: 'crew', name: 'crew', home: '/w', members: [member, { ...member, id: 'other', role: 'specialist', model_source: 'session', resolved_provider: 'google', resolved_model: 'live-flash' }] }
    const client = fakeClient({ respond: (method) => method === 'talkoot.get' ? view : method === 'talkoot.room' ? { lines: [], total: 0 } : method === 'talkoot.inbox' ? { cards: [] } : {} })
    render(<TalkootTeam client={client} hub={new TalkootHub()} id="crew" generation={1} person="sothr" onOpenSession={() => { throw new Error('opened session') }} />)
    await screen.findByText('google / live-flash (live session)')
    expect(document.querySelector('.talkoot-side')!.textContent).toContain('anthropic / resolved-opus')
    expect(document.querySelector('.talkoot-main-meta')!.textContent).toContain('anthropic / resolved-opus')
    expect(client.sent('models.list')).toHaveLength(0)
    expect(client.sent('sessions.create')).toHaveLength(0)
  })

  it('marks old-daemon and resolution-error models as unknown, not a backend claim', () => {
    expect(memberModelSummary({ ...member, resolved_model: undefined })).toBe('Tier: strong (model not reported)')
    expect(memberModelSummary({ ...member, resolved_model: undefined, model_problem: 'unknown id' })).toBe('Model unavailable: unknown id')
  })

  it('disables the model, search, and tier fields for a spectator', async () => {
    card(member, clientFor(), false)
    for (const label of ['Model', 'Search models', 'Tier']) expect((screen.getByLabelText(label) as HTMLInputElement).disabled).toBe(true)
    expect(screen.queryByText('Save changes')).toBeNull()
  })
})
