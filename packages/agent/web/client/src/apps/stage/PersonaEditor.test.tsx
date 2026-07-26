// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import type { PersonaSummary, PersonaView } from '../../platform/ctrlproto/types'
import { PersonaEditor, duplicateName, formFromView, slugPreview } from './PersonaEditor'
import { PersonaSheet } from './PersonaSheet'

const BUILTIN: PersonaSummary = { name: 'Seppä', ref: 'Seppä', origin: 'built-in', emoji: '🛠️' }
const MINE: PersonaSummary = { name: 'Scratch', ref: 'Scratch', origin: 'user', editable: true }

const VIEW: PersonaView = {
  ...BUILTIN,
  charter: 'Read the card and its lint.',
  good_for: ['card craft', 'lint'],
  avoid_for: ['roleplay'],
  recommended_skills: ['cards'],
  pronunciation: 'SEP-pah',
  introduction: 'I am the card doctor.',
  summary: 'Card-craft doctor',
  specialty: 'cards',
}

function stubClient(view: PersonaView = VIEW) {
  return fakeClient({ respond: (method) => (method === 'personas.get' ? view : {}) })
}

afterEach(cleanup)

describe('duplicateName', () => {
  // personas.create only checks the USER layer, so a persona named after a
  // built-in is accepted and silently shadows it — the exact permanent fork the
  // duplicate flow exists to avoid. The collision check has to be ours, against
  // the whole roster.
  it('proposes a copy name that is free', () => {
    expect(duplicateName('Seppä', ['Seppä'])).toBe('Seppä (my copy)')
  })

  it('keeps going when the obvious copy name is taken too', () => {
    expect(duplicateName('Seppä', ['Seppä', 'Seppä (my copy)'])).toBe('Seppä (my copy 2)')
  })

  it('ignores case when deciding what is taken', () => {
    expect(duplicateName('Seppä', ['seppä (MY COPY)'])).toBe('Seppä (my copy 2)')
  })
})

describe('slugPreview', () => {
  it('mirrors the daemon: lowercase, alphanumeric only, capped', () => {
    expect(slugPreview('Seppä The Smith')).toBe('seppthesmith')
    expect(slugPreview('a'.repeat(40))).toHaveLength(32)
  })

  // A name of nothing but non-latin characters slugs to "", and the daemon
  // answers "no usable filename" — an error about files, for a form that never
  // mentioned files. The editor catches it first.
  it('reports an unusable name as empty rather than guessing', () => {
    expect(slugPreview('日本語')).toBe('')
  })
})

describe('formFromView', () => {
  // A write REPLACES the persona file, so every field the editor holds must be
  // re-sent. Missing list fields have to become [] rather than undefined or the
  // spread drops them and the save silently erases them.
  it('fills every field so a save cannot erase one', () => {
    const f = formFromView({ name: 'Bare', ref: 'Bare', origin: 'user' })
    expect(f.good_for).toEqual([])
    expect(f.avoid_for).toEqual([])
    expect(f.recommended_skills).toEqual([])
    expect(f.charter).toBe('')
    expect(f.immersive).toBe(false)
  })
})

describe('PersonaSheet provenance gating', () => {
  // The daemon would happily let personas.edit shadow a built-in by slug. The UI
  // is the only thing preventing a permanent, invisible fork of the embedded
  // crew, so this gate is load-bearing, not cosmetic.
  it('offers Duplicate — never Edit or Delete — for a built-in', () => {
    render(
      <PersonaSheet client={stubClient()} persona={BUILTIN} onClose={() => {}} onEdit={() => {}} onDuplicate={() => {}} onDelete={() => {}} />,
    )
    expect(screen.getByText('⧉ Duplicate')).toBeTruthy()
    expect(screen.queryByText('✎ Edit')).toBeNull()
    expect(screen.queryByText('Delete persona')).toBeNull()
  })

  it('offers Edit and Delete — never Duplicate — for one of yours', async () => {
    render(
      <PersonaSheet
        client={stubClient({ ...VIEW, ...MINE })}
        persona={MINE}
        onClose={() => {}}
        onEdit={() => {}}
        onDuplicate={() => {}}
        onDelete={() => {}}
      />,
    )
    expect(screen.getByText('✎ Edit')).toBeTruthy()
    expect(screen.queryByText('⧉ Duplicate')).toBeNull()
    expect(await screen.findByText('Delete persona')).toBeTruthy()
  })
})

describe('PersonaEditor', () => {
  it('duplicates a built-in under a new name, via create', async () => {
    const client = stubClient()
    render(<PersonaEditor client={client} persona={BUILTIN} duplicate taken={['Seppä']} onClose={() => {}} onSaved={() => {}} />)

    // The charter comes along — that is the point of duplicating — but the name
    // must not, or create would shadow the built-in it copied.
    const name = (await screen.findByDisplayValue('Seppä (my copy)')) as HTMLInputElement
    expect(name).toBeTruthy()
    expect(screen.getByDisplayValue('Read the card and its lint.')).toBeTruthy()

    fireEvent.click(screen.getByText('Create persona'))
    await waitFor(() => expect(client.send.mock.calls.some((c) => c[0] === 'personas.create')).toBe(true))
    const params = client.send.mock.calls.find((c) => c[0] === 'personas.create')![1] as Record<string, unknown>
    expect(params.name).toBe('Seppä (my copy)')
    // Every field re-sent: a write replaces the file, so an omitted field is
    // erased rather than preserved.
    expect(params.charter).toBe('Read the card and its lint.')
    expect(params.good_for).toEqual(['card craft', 'lint'])
    expect(params.pronunciation).toBe('SEP-pah')
  })

  it('edits one of yours in place, via edit', async () => {
    const client = stubClient({ ...VIEW, ...MINE })
    render(<PersonaEditor client={client} persona={MINE} taken={['Scratch']} onClose={() => {}} onSaved={() => {}} />)

    await screen.findByDisplayValue('Scratch')
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.send.mock.calls.some((c) => c[0] === 'personas.edit')).toBe(true))
    // Editing must NOT rename — that would create a second persona and leave the
    // original behind.
    expect(client.send.mock.calls.find((c) => c[0] === 'personas.edit')![1]).toMatchObject({ name: 'Scratch' })
  })

  // A write REPLACES the persona file, so a field the editor forgets to re-send
  // is ERASED. Group is the newest such field, and the failure would be silent:
  // save an unrelated typo fix and the persona quietly falls off its shelf.
  it('carries the group through an edit that never touched it', async () => {
    const client = stubClient({ ...VIEW, ...MINE, group: 'Review' })
    render(<PersonaEditor client={client} persona={MINE} taken={['Scratch']} onClose={() => {}} onSaved={() => {}} />)

    await screen.findByDisplayValue('Scratch')
    fireEvent.input(screen.getByLabelText(/Specialty/i, { selector: 'input' }), { target: { value: 'scratchpad' } })
    fireEvent.click(screen.getByText('Save changes'))

    await waitFor(() => expect(client.send.mock.calls.some((c) => c[0] === 'personas.edit')).toBe(true))
    expect(client.send.mock.calls.find((c) => c[0] === 'personas.edit')![1]).toMatchObject({ group: 'Review' })
  })

  it('files a persona on a shelf you type, and suggests the ones that exist', async () => {
    const client = stubClient({ ...VIEW, ...MINE })
    render(
      <PersonaEditor
        client={client}
        persona={MINE}
        taken={['Scratch']}
        groups={['Coding', 'Review']}
        onClose={() => {}}
        onSaved={() => {}}
      />,
    )

    const group = (await screen.findByLabelText(/Group/i, { selector: 'input' })) as HTMLInputElement
    // A datalist, not a select — the shelf you want may not exist yet.
    expect(group.getAttribute('list')).toBe('stage-persona-groups')
    expect([...document.querySelectorAll('#stage-persona-groups option')].map((o) => o.getAttribute('value'))).toEqual([
      'Coding',
      'Review',
    ])

    fireEvent.input(group, { target: { value: 'My crew' } })
    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.send.mock.calls.some((c) => c[0] === 'personas.edit')).toBe(true))
    expect(client.send.mock.calls.find((c) => c[0] === 'personas.edit')![1]).toMatchObject({ group: 'My crew' })
  })

  it('refuses to save a name already in the roster', async () => {
    const client = stubClient()
    render(<PersonaEditor client={client} taken={['Seppä']} onClose={() => {}} onSaved={() => {}} />)

    const name = screen.getByLabelText(/Name/i, { selector: 'input' }) as HTMLInputElement
    fireEvent.input(name, { target: { value: 'Seppä' } })

    const save = screen.getByText('Create persona') as HTMLButtonElement
    expect(save.disabled).toBe(true)
    expect(screen.getByText(/would hide the built-in/)).toBeTruthy()
    fireEvent.click(save)
    expect(client.send.mock.calls.some((c) => c[0] === 'personas.create')).toBe(false)
  })

  it('refuses a name with nothing to build a filename from', () => {
    const client = stubClient()
    render(<PersonaEditor client={client} taken={[]} onClose={() => {}} onSaved={() => {}} />)

    fireEvent.input(screen.getByLabelText(/Name/i, { selector: 'input' }), { target: { value: '日本語' } })
    expect((screen.getByText('Create persona') as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText(/no letters or digits/)).toBeTruthy()
  })
})
