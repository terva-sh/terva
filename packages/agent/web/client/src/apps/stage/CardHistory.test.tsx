// @vitest-environment happy-dom
//
// The revision list and its restore, which exist for the case where a machine
// pass — the doctor, enrich — rewrote fields the user never typed and the state
// worth getting back is one they never read.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { CardEditor } from './CardEditor'
import { CardHistory, changedLabel, whenLabel } from './CardHistory'

afterEach(cleanup)

const REVISIONS = [
  { ref: '1700000002000', saved: '2026-07-24T12:00:00Z', bytes: 220, name: 'Kobeni', fields: ['personality'] },
  { ref: '1700000001000', saved: '2026-07-23T12:00:00Z', bytes: 210, name: 'Kobeni Draft', fields: ['personality', 'first_mes', 'tags', 'character_book'] },
]

// The card as SAVED — what a revision is compared against.
const CURRENT = { name: 'Kobeni', personality: 'steady', first_mes: 'hello' }

const REVISION_DETAIL = {
  ref: '1700000002000',
  saved: '2026-07-24T12:00:00Z',
  name: 'Kobeni',
  fields: ['personality'],
  raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'anxious', first_mes: 'hello' } },
}

const RESTORED = {
  id: 'c1',
  name: 'Kobeni',
  greetings: 1,
  raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'restored' } },
}

function stub(revisions: unknown = REVISIONS) {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.history':
          return { revisions }
        case 'cards.restore':
          return RESTORED
        case 'cards.revision':
          return REVISION_DETAIL
        default:
          return {}
      }
    },
  })
}

describe('CardHistory', () => {
  it('lists a card’s revisions', async () => {
    const client = stub()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={vi.fn()} />)

    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    expect(client.last('cards.history')?.params).toEqual({ id: 'c1' })
    expect(await screen.findByText('Earlier versions')).toBeTruthy()
    expect(screen.getAllByText('Restore all').length).toBe(2)
    // A revision whose name differs from the card's current one says so — the
    // point of storing the name per revision rather than labelling every row
    // with the name the card happens to have now.
    expect(screen.getByText('named Kobeni Draft')).toBeTruthy()
    expect(screen.queryByText('named Kobeni')).toBeNull()
  })

  it('teaches the feature instead of vanishing when a card has no revisions', async () => {
    const client = stub([])
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={vi.fn()} />)

    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    expect(await screen.findByText(/Saved changes are kept here/)).toBeTruthy()
    expect(screen.queryByText('Restore all')).toBeNull()
  })

  it('asks before restoring, because a restore replaces the unsaved draft', async () => {
    const client = stub()
    const onRestored = vi.fn()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={onRestored} onUseField={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))

    fireEvent.click(screen.getAllByText('Restore all')[0])
    // The first click only arms it — nothing has been sent.
    expect(screen.getByText('Replaces what is in the editor now.')).toBeTruthy()
    expect(client.sent('cards.restore').length).toBe(0)

    fireEvent.click(screen.getByText('Cancel'))
    expect(screen.queryByText('Replaces what is in the editor now.')).toBeNull()
    expect(client.sent('cards.restore').length).toBe(0)
    expect(onRestored).not.toHaveBeenCalled()
  })

  it('restores by ref, hands the card back, and re-reads the history', async () => {
    const client = stub()
    const onRestored = vi.fn()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={onRestored} onUseField={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))

    fireEvent.click(screen.getAllByText('Restore all')[0])
    // Scope to the armed row: the OTHER revision still offers its own untouched
    // "Restore", so an unscoped query matches two buttons that do different
    // things.
    const confirm = document.querySelector('.stage-cardhistory__confirm') as HTMLElement
    fireEvent.click(within(confirm).getByText('Restore all'))

    await waitFor(() => expect(client.sent('cards.restore').length).toBe(1))
    expect(client.last('cards.restore')?.params).toEqual({ id: 'c1', ref: '1700000002000' })
    // The editor is reseeded from the card the server returned, not from a
    // local guess about what the revision held.
    await waitFor(() => expect(onRestored).toHaveBeenCalledWith(RESTORED))
    // …and the list refreshes, because the restore added a revision of its own.
    await waitFor(() => expect(client.sent('cards.history').length).toBe(2))
  })

  it('says nothing at all when the daemon does not know the verb', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        if (method === 'cards.history') throw new Error('unsupported')
        return {}
      },
    })
    const { container } = render(
      <CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={vi.fn()} />,
    )
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    // An older daemon is not an error to show the user — the section is absent.
    await waitFor(() => expect(container.querySelector('.stage-cardhistory')).toBeNull())
  })

  it('asks for nothing until the card exists', () => {
    const client = stub()
    const { container } = render(
      <CardHistory client={client} cardId="" currentName="" currentData={{}} onRestored={vi.fn()} onUseField={vi.fn()} />,
    )
    // A character being created has no id yet, so there is nothing to ask about.
    expect(client.sent('cards.history').length).toBe(0)
    expect(container.querySelector('.stage-cardhistory')).toBeNull()
  })
})

describe('whenLabel', () => {
  const at = (iso: string) => Date.parse(iso)

  it('buckets an age into something readable', () => {
    const now = at('2026-07-24T12:00:00Z')
    expect(whenLabel('2026-07-24T11:59:30Z', now)).toBe('just now')
    expect(whenLabel('2026-07-24T11:58:00Z', now)).toBe('2 minutes ago')
    expect(whenLabel('2026-07-24T11:00:00Z', now)).toBe('1 hour ago')
    expect(whenLabel('2026-07-24T09:00:00Z', now)).toBe('3 hours ago')
    expect(whenLabel('2026-07-22T12:00:00Z', now)).toBe('2 days ago')
  })

  it('never reports a zero, and falls back to a date once a week is up', () => {
    const now = at('2026-07-24T12:00:00Z')
    // The boundaries: 60s is a minute, 60m is an hour, 24h is a day. An
    // off-by-one here renders "0 minutes ago", which reads as broken.
    expect(whenLabel('2026-07-24T11:59:00Z', now)).toBe('1 minute ago')
    expect(whenLabel('2026-07-24T11:00:00Z', now)).toBe('1 hour ago')
    expect(whenLabel('2026-07-23T12:00:00Z', now)).toBe('1 day ago')
    expect(whenLabel('2026-07-01T12:00:00Z', now)).toBe(new Date(at('2026-07-01T12:00:00Z')).toLocaleDateString())
    expect(whenLabel('not a date', now)).toBe('')
  })
})

// The wiring, not the component: the section has to actually be mounted by the
// editor, and a restore has to reseed the form. A verb no screen calls is the
// built-but-unwired failure this repo keeps finding.
describe('CardEditor mounts the history', () => {
  const CARD = { id: 'c1', name: 'Kobeni', greetings: 1 }

  function editorClient() {
    return fakeClient({
      respond: (method: Verb) => {
        switch (method) {
          case 'cards.get':
            return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'cold' } } }
          case 'cards.lint':
            return { findings: [] }
          case 'cards.history':
            return { revisions: [{ ref: '1700000002000', saved: '2026-07-24T12:00:00Z', bytes: 220, name: 'Kobeni', fields: ['personality'] }] }
          case 'cards.revision':
            return {
              ref: '1700000002000',
              saved: '2026-07-24T12:00:00Z',
              name: 'Kobeni',
              fields: ['personality'],
              raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'anxious' } },
            }
          case 'cards.edit':
            return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'anxious' } } }
          case 'cards.restore':
            return RESTORED
          default:
            return {}
        }
      },
    })
  }

  it('asks for the open card’s revisions', async () => {
    const client = editorClient()
    render(<CardEditor client={client} card={CARD} onClose={vi.fn()} onSaved={vi.fn()} />)

    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    expect(client.last('cards.history')?.params).toEqual({ id: 'c1' })
    expect(await screen.findByText('Earlier versions')).toBeTruthy()
  })

  it('reseeds the form from the restored card and tells the host to refresh', async () => {
    const client = editorClient()
    const onSaved = vi.fn()
    render(<CardEditor client={client} card={CARD} onClose={vi.fn()} onSaved={onSaved} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))

    const personality = (await screen.findByDisplayValue('cold')) as HTMLTextAreaElement
    expect(personality).toBeTruthy()

    fireEvent.click(screen.getAllByText('Restore all')[0])
    const confirm = document.querySelector('.stage-cardhistory__confirm') as HTMLElement
    fireEvent.click(within(confirm).getByText('Restore all'))

    await waitFor(() => expect(client.sent('cards.restore').length).toBe(1))
    // The editor now shows the RESTORED card, not the one it was holding.
    await waitFor(() => expect(screen.getByDisplayValue('restored')).toBeTruthy())
    expect(screen.queryByDisplayValue('cold')).toBeNull()
    // onSaved means "the library moved, re-read it" — a restore moves it.
    expect(onSaved).toHaveBeenCalled()
  })
})

describe('CardHistory diff and per-field revert', () => {
  it('says what each revision differs in, without fetching any of them', async () => {
    const client = stub()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))

    // Few fields: name them, because "personality" is actionable where "1 field" is not.
    expect(screen.getByText('Personality')).toBeTruthy()
    // Many: fall back to a count rather than an unreadable line.
    expect(screen.getByText('4 fields differ')).toBeTruthy()
    // The list is metadata only — a card with a large lorebook must not ship
    // ten copies of it to render a list nobody has opened.
    expect(client.sent('cards.revision').length).toBe(0)
  })

  it('fetches one revision on expand and shows then/now', async () => {
    const client = stub()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))

    fireEvent.click(screen.getByText('Personality'))
    await waitFor(() => expect(client.sent('cards.revision').length).toBe(1))
    expect(client.last('cards.revision')?.params).toEqual({ id: 'c1', ref: '1700000002000' })

    // then = the revision, now = the card as saved.
    expect(await screen.findByText('anxious')).toBeTruthy()
    expect(screen.getByText('steady')).toBeTruthy()

    // Clicking again collapses it and asks for nothing more.
    fireEvent.click(screen.getAllByText('Personality')[0])
    await waitFor(() => expect(screen.queryByText('anxious')).toBeNull())
    expect(client.sent('cards.revision').length).toBe(1)
  })

  it('copies one field into the editor as a draft, writing nothing', async () => {
    const client = stub()
    const onUseField = vi.fn()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={onUseField} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    fireEvent.click(screen.getByText('Personality'))
    await waitFor(() => expect(client.sent('cards.revision').length).toBe(1))

    fireEvent.click(await screen.findByText('Use this'))

    // The revision's RAW value goes to the editor, not the joined display text.
    expect(onUseField).toHaveBeenCalledWith('personality', 'anxious')
    // And nothing was written: this is a draft the user still has to save.
    expect(client.sent('cards.edit').length).toBe(0)
    expect(client.sent('cards.restore').length).toBe(0)
    expect(await screen.findByText('Copied ✓')).toBeTruthy()
  })

  it('refuses to copy a field the editor cannot show', async () => {
    const client = fakeClient({
      respond: (method: Verb) => {
        switch (method) {
          case 'cards.history':
            return { revisions: [{ ref: '1700000003000', saved: '2026-07-24T12:00:00Z', bytes: 300, fields: ['character_book'] }] }
          case 'cards.revision':
            return {
              ref: '1700000003000',
              saved: '2026-07-24T12:00:00Z',
              fields: ['character_book'],
              raw: { spec: 'chara_card_v2', data: { character_book: { entries: [{ keys: ['k'], content: 'c' }] } } },
            }
          default:
            return {}
        }
      },
    })
    const onUseField = vi.fn()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={onUseField} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    fireEvent.click(screen.getByText('Lorebook'))
    await waitFor(() => expect(client.sent('cards.revision').length).toBe(1))

    // Copying a lorebook alone would change the saved card with nothing on
    // screen to show it — so the row reports the change and points at restore.
    expect(await screen.findByText('restore all to change')).toBeTruthy()
    expect(screen.queryByText('Use this')).toBeNull()
  })

// A scalar field cannot tell these apart — its display text IS its value. Only
// a list field can: sending the joined text back would put the string
// "office, cursed" into a field the editor types as string[].
it('hands back a list field’s raw value, not its display text', async () => {
  const client = fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.history':
          return { revisions: [{ ref: '1700000004000', saved: '2026-07-24T12:00:00Z', bytes: 200, fields: ['tags'] }] }
        case 'cards.revision':
          return {
            ref: '1700000004000',
            saved: '2026-07-24T12:00:00Z',
            fields: ['tags'],
            raw: { spec: 'chara_card_v2', data: { tags: ['office', 'cursed'] } },
          }
        default:
          return {}
      }
    },
  })
  const onUseField = vi.fn()
  render(
    <CardHistory
      client={client}
      cardId="c1"
      currentName="Kobeni"
      currentData={{ tags: ['office'] }}
      onRestored={vi.fn()}
      onUseField={onUseField}
    />,
  )
  await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
  fireEvent.click(screen.getByText('Tags'))
  await waitFor(() => expect(client.sent('cards.revision').length).toBe(1))

  // The joined text is what the reader sees…
  expect(await screen.findByText('office, cursed')).toBeTruthy()
  fireEvent.click(screen.getByText('Use this'))
  // …the array is what the editor gets.
  expect(onUseField).toHaveBeenCalledWith('tags', ['office', 'cursed'])
})

  it('re-reads the list when the stored card moves', async () => {
    const client = stub()
    const { rerender } = render(
      <CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} reloadKey={0} onRestored={vi.fn()} onUseField={vi.fn()} />,
    )
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))

    // A save adds a revision AND changes what every other one differs in, so a
    // list left alone would be quietly wrong.
    rerender(
      <CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} reloadKey={1} onRestored={vi.fn()} onUseField={vi.fn()} />,
    )
    await waitFor(() => expect(client.sent('cards.history').length).toBe(2))
  })
})

describe('CardEditor per-field revert', () => {
  const CARD = { id: 'c1', name: 'Kobeni', greetings: 1 }

  function editorClient() {
    return fakeClient({
      respond: (method: Verb) => {
        switch (method) {
          case 'cards.get':
            return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'cold' } } }
          case 'cards.lint':
            return { findings: [] }
          case 'cards.history':
            return { revisions: [{ ref: '1700000002000', saved: '2026-07-24T12:00:00Z', bytes: 220, name: 'Kobeni', fields: ['personality'] }] }
          case 'cards.revision':
            return {
              ref: '1700000002000',
              saved: '2026-07-24T12:00:00Z',
              name: 'Kobeni',
              fields: ['personality'],
              raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'anxious' } },
            }
          case 'cards.edit':
            return { id: 'c1', name: 'Kobeni', raw: { spec: 'chara_card_v2', data: { name: 'Kobeni', personality: 'anxious' } } }
          default:
            return {}
        }
      },
    })
  }

  it('lands the old value in the real input, and saves it with everything else', async () => {
    const client = editorClient()
    render(<CardEditor client={client} card={CARD} onClose={vi.fn()} onSaved={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    // The editor compares against the card AS SAVED, which it already holds.
    expect(await screen.findByDisplayValue('cold')).toBeTruthy()

    // Scope to the history section: 'Personality' is ALSO the label on the
    // editor's own textarea, so an unscoped query matches two unrelated nodes.
    const history = within(document.querySelector('.stage-cardhistory') as HTMLElement)
    fireEvent.click(history.getByText('Personality'))
    await waitFor(() => expect(client.sent('cards.revision').length).toBe(1))
    fireEvent.click(await history.findByText('Use this'))

    // This is the whole point of a per-field revert: the value reaches the
    // input the user is looking at, not just the document underneath it.
    await waitFor(() => expect(screen.getByDisplayValue('anxious')).toBeTruthy())
    expect(screen.queryByDisplayValue('cold')).toBeNull()
    // Nothing was written yet — it is a draft.
    expect(client.sent('cards.edit').length).toBe(0)

    fireEvent.click(screen.getByText('Save changes'))
    await waitFor(() => expect(client.sent('cards.edit').length).toBe(1))
    const sent = client.last('cards.edit')?.params as { card: { data: Record<string, unknown> } }
    expect(sent.card.data.personality).toBe('anxious')
    // …and a save re-reads the history, because it added a revision of its own.
    await waitFor(() => expect(client.sent('cards.history').length).toBe(2))
  })
})

describe('CardHistory portrait', () => {
  const portraitClient = () =>
    fakeClient({
      respond: (method: Verb) => {
        switch (method) {
          case 'cards.history':
            // A revision that replaced ONLY the picture: no card field moved.
            return { revisions: [{ ref: '1700000005000', saved: '2026-07-24T12:00:00Z', bytes: 200, fields: [], portrait: true }] }
          case 'cards.revision':
            return {
              ref: '1700000005000',
              saved: '2026-07-24T12:00:00Z',
              fields: [],
              portrait: true,
              raw: { spec: 'chara_card_v2', data: { name: 'Kobeni' } },
            }
          default:
            return {}
        }
      },
    })

  it('never calls a picture-only revision identical', async () => {
    const client = portraitClient()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))

    // The whole bug in one assertion: no card field moved, but the card is not
    // the same, and saying "identical to now" is the silence being fixed.
    expect(screen.queryByText('identical to now')).toBeNull()
    expect(screen.getByText('Portrait')).toBeTruthy()
  })

  it('offers the portrait only through a whole restore', async () => {
    const client = portraitClient()
    render(<CardHistory client={client} cardId="c1" currentName="Kobeni" currentData={CURRENT} onRestored={vi.fn()} onUseField={vi.fn()} />)
    await waitFor(() => expect(client.sent('cards.history').length).toBe(1))
    fireEvent.click(screen.getByText('Portrait'))
    await waitFor(() => expect(client.sent('cards.revision').length).toBe(1))

    // The picture is a separate file, not a field the editor renders, so it can
    // never be copied in on its own.
    expect(await screen.findByText('restore all to change')).toBeTruthy()
    expect(screen.queryByText('Use this')).toBeNull()
  })
})

describe('changedLabel', () => {
  it('counts the portrait as a difference', () => {
    expect(changedLabel([], false)).toBe('identical to now')
    expect(changedLabel([], true)).toBe('Portrait')
    expect(changedLabel(['personality'], true)).toBe('Personality, Portrait')
    // Three fields plus the picture tips it over into a count.
    expect(changedLabel(['personality', 'scenario', 'tags'], true)).toBe('4 fields differ')
  })
})
