// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { UserPersonaView } from '../../platform/ctrlproto/types'
import { YouEditor, type ScenePersona } from './YouEditor'

const KIRA: UserPersonaView = { ref: 'kira', name: 'Kira', description: 'A wary courier.', pronouns: 'she/her', default: true }
const MARA: UserPersonaView = { ref: 'mara', name: 'Mara', description: 'A dockside fixer.' }

function mount(scene: ScenePersona | null, personas: UserPersonaView[] = [KIRA, MARA]) {
  const client = fakeClient({
    respond: (method, params) => {
      if (method === 'userpersonas.list') return { personas }
      if (method === 'userpersonas.save') {
        const p = params as { name: string; description?: string }
        return { ref: p.name.toLowerCase(), name: p.name, description: p.description }
      }
      return {}
    },
  })
  render(<YouEditor client={client} ready scene={scene} />)
  return client
}

const scene = (over: Partial<ScenePersona> = {}): ScenePersona => ({
  session: 's1',
  name: 'Kira',
  description: 'A wary courier.',
  gender: '',
  pronouns: 'she/her',
  ...over,
})

const nameBox = () => document.querySelector('.stage-you__name') as HTMLInputElement
const descBox = () => document.querySelector('.stage-you__desc') as HTMLTextAreaElement

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

// In a scene the editor is a live steering surface, not a form: it edits THAT
// session's persona and commits on blur, the way the drawer's You tab always has.
describe('YouEditor — steering the scene you came from', () => {
  it('commits an edited description to the session', async () => {
    const client = mount(scene())
    fireEvent.input(descBox(), { target: { value: 'Wearier now.' } })
    fireEvent.blur(descBox())

    await waitFor(() => expect(client.last('user.bind')).toBeDefined())
    const bind = client.last('user.bind')!
    expect(bind.sess).toBe('s1')
    expect(bind.params).toMatchObject({ name: 'Kira', description: 'Wearier now.', pronouns: 'she/her' })
  })

  // A bind with a changed name rebuilds the cached prompt prefix, so a blur that
  // changed nothing must not fire one — tabbing through the form is free.
  it('sends nothing when a blur changed nothing', async () => {
    const client = mount(scene())
    fireEvent.blur(nameBox())
    fireEvent.blur(descBox())
    await waitFor(() => expect(client.sent('userpersonas.list').length).toBe(1))
    expect(client.sent('user.bind')).toHaveLength(0)
  })

  // Picking a saved persona binds it BY REF: the daemon owns resolving a stored
  // identity, so the client never re-sends fields it might have stale.
  it('plays as a saved persona by ref and re-seeds the form', async () => {
    const client = mount(scene())
    fireEvent.click(await screen.findByTitle('Play as Mara in this scene'))

    await waitFor(() => expect(client.last('user.bind')).toBeDefined())
    expect(client.last('user.bind')!.params).toEqual({ ref: 'mara' })
    expect(client.last('user.bind')!.sess).toBe('s1')
    expect(nameBox().value).toBe('Mara')
    expect(descBox().value).toBe('A dockside fixer.')
  })

  // ...and the re-seed must not then be committed back: a bind by ref already
  // set those fields, so an inline bind echoing them is a redundant prompt
  // rebuild on the very next blur.
  it('does not re-commit the fields a ref bind just installed', async () => {
    const client = mount(scene())
    fireEvent.click(await screen.findByTitle('Play as Mara in this scene'))
    await waitFor(() => expect(client.last('user.bind')).toBeDefined())

    fireEvent.blur(nameBox())
    await waitFor(() => expect(client.sent('user.bind')).toHaveLength(1))
  })

  // The row the scene is playing as is marked, because the list is otherwise
  // silent about which of these you currently are.
  it('marks the persona the scene is playing as', async () => {
    mount(scene({ name: 'Mara' }))
    const rows = await screen.findAllByRole('listitem')
    expect(rows.find((r) => r.textContent?.includes('Mara'))?.className).toContain('stage-you__row--playing')
    expect(rows.find((r) => r.textContent?.includes('Kira'))?.className).not.toContain('stage-you__row--playing')
  })

  // Keeping a scene identity in the library is name-keyed (no ref): the scene is
  // the thing being edited, and its name decides which stored persona it is.
  it('keeps the scene persona in the library without a ref', async () => {
    const client = mount(scene())
    fireEvent.input(nameBox(), { target: { value: 'Kiran' } })
    fireEvent.click(screen.getByText('Keep in your personas'))

    await waitFor(() => expect(client.last('userpersonas.save')).toBeDefined())
    expect(client.last('userpersonas.save')!.params).toMatchObject({ ref: '', name: 'Kiran' })
  })
})

// From the Library there is no scene to steer, so the editor edits a SAVED
// persona and an explicit Save commits it.
describe('YouEditor — editing the library', () => {
  it('does not bind anything without a scene', async () => {
    const client = mount(null)
    fireEvent.click(await screen.findByTitle('Edit Mara'))
    fireEvent.input(descBox(), { target: { value: 'Changed.' } })
    fireEvent.blur(descBox())

    await waitFor(() => expect(nameBox().value).toBe('Mara'))
    expect(client.sent('user.bind')).toHaveLength(0)
  })

  // The whole reason userpersonas.save grew a ref: without one, a changed name
  // is indistinguishable from a new persona, so the rename wrote a second row
  // and left the first behind.
  it('carries the edited persona’s ref so a changed name renames it', async () => {
    const client = mount(null)
    fireEvent.click(await screen.findByTitle('Edit Kira'))
    fireEvent.input(nameBox(), { target: { value: 'Kiran' } })
    fireEvent.click(screen.getByText('Save'))

    await waitFor(() => expect(client.last('userpersonas.save')).toBeDefined())
    expect(client.last('userpersonas.save')!.params).toMatchObject({ ref: 'kira', name: 'Kiran' })
  })

  // A new persona has no ref to carry — sending the previously-edited one would
  // rename THAT persona into the new one's name, silently destroying it.
  it('drops the ref when + New persona starts a fresh one', async () => {
    const client = mount(null)
    fireEvent.click(await screen.findByTitle('Edit Kira'))
    fireEvent.click(screen.getByText('+ New persona'))
    expect(nameBox().value).toBe('')

    fireEvent.input(nameBox(), { target: { value: 'Vess' } })
    fireEvent.click(screen.getByText('Save'))
    await waitFor(() => expect(client.last('userpersonas.save')).toBeDefined())
    expect(client.last('userpersonas.save')!.params).toMatchObject({ ref: '', name: 'Vess' })
  })

  // The star is a toggle, not a one-way switch: clicking the current default
  // clears it (an empty ref), so "no default" is reachable.
  it('toggles the default off with an empty ref', async () => {
    const client = mount(null)
    fireEvent.click(await screen.findByTitle('Stop pre-filling Kira into new chats'))
    await waitFor(() => expect(client.last('userpersonas.set_default')).toBeDefined())
    expect(client.last('userpersonas.set_default')!.params).toEqual({ ref: '' })

    fireEvent.click(screen.getByTitle('Pre-fill Mara into every new chat'))
    await waitFor(() => expect(client.sent('userpersonas.set_default')).toHaveLength(2))
    expect(client.last('userpersonas.set_default')!.params).toEqual({ ref: 'mara' })
  })

  it('confirms before deleting, and clears the editor pointed at the deleted row', async () => {
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(true))
    const client = mount(null)
    fireEvent.click(await screen.findByTitle('Edit Kira'))
    expect(nameBox().value).toBe('Kira')

    fireEvent.click(screen.getByTitle('Delete Kira'))
    await waitFor(() => expect(client.last('userpersonas.delete')).toBeDefined())
    expect(client.last('userpersonas.delete')!.params).toEqual({ ref: 'kira' })
    await waitFor(() => expect(nameBox().value).toBe(''))
  })

  it('deletes nothing when the confirm is declined', async () => {
    vi.stubGlobal('confirm', vi.fn().mockReturnValue(false))
    const client = mount(null)
    fireEvent.click(await screen.findByTitle('Delete Mara'))
    await waitFor(() => expect(client.sent('userpersonas.list')).toHaveLength(1))
    expect(client.sent('userpersonas.delete')).toHaveLength(0)
  })
})
