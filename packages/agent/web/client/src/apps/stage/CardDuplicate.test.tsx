// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import { CardSheet } from './CardSheet'
import { copyName } from './Library'

// cards.duplicate exists because the two obvious ways to copy a card fail
// quietly: re-importing an export returns the original (ids are content
// addressed, so a copy that changed nothing IS the original), and rebuilding
// from the card JSON drops the portrait. The daemon refuses the first outright,
// which makes proposing a free name a workflow concern rather than a nicety —
// without it the very first click meets a refusal.

describe('copyName', () => {
  it('proposes a copy of the name', () => {
    expect(copyName('Kobeni', ['Kobeni'])).toBe('Kobeni (copy)')
  })

  it('counts upward past copies already made', () => {
    expect(copyName('Kobeni', ['Kobeni', 'Kobeni (copy)'])).toBe('Kobeni (copy 2)')
    expect(copyName('Kobeni', ['Kobeni', 'Kobeni (copy)', 'Kobeni (copy 2)'])).toBe('Kobeni (copy 3)')
  })

  // The daemon files by a slug that folds case, so "kobeni (COPY)" already
  // occupies the name a case-sensitive check would hand out.
  it('ignores case when deciding a name is taken', () => {
    expect(copyName('Kobeni', ['kobeni (COPY)'])).toBe('Kobeni (copy 2)')
  })

  it('ignores surrounding whitespace on the names it compares', () => {
    expect(copyName('Kobeni', ['  Kobeni (copy)  '])).toBe('Kobeni (copy 2)')
  })

  // A library with nothing in it must still get a usable proposal — the empty
  // list is the first-ever duplicate, not an error.
  it('proposes the first copy against an empty library', () => {
    expect(copyName('Kobeni', [])).toBe('Kobeni (copy)')
  })
})

// The sheet layer. A helper test cannot see a button wired to nothing (the #279
// lesson), so the control itself is exercised here.
function stub() {
  return fakeClient({
    respond: (method: Verb) => {
      switch (method) {
        case 'cards.get':
          return { id: 'c1', name: 'Kobeni', raw: { data: {} } }
        case 'cards.lint':
          return { findings: [] }
        case 'models.list':
          return { models: [] }
        default:
          return {}
      }
    },
  })
}

const card = { id: 'c1', name: 'Kobeni', greetings: 1 }

afterEach(cleanup)

describe('CardSheet duplicate action', () => {
  it('fires onDuplicate when the control is used', async () => {
    const onDuplicate = vi.fn()
    render(<CardSheet client={stub()} card={card} busy={false} onClose={() => {}} onStart={() => {}} onDuplicate={onDuplicate} />)
    const btn = await waitFor(() => screen.getByText('⧉ Duplicate card'))
    fireEvent.click(btn)
    expect(onDuplicate).toHaveBeenCalledTimes(1)
  })

  // Every other whole-card action on this sheet is optional and hidden when the
  // host does not offer it; a control that looks live and does nothing is worse
  // than an absent one.
  it('offers no duplicate control when the host does not handle it', async () => {
    render(<CardSheet client={stub()} card={card} busy={false} onClose={() => {}} onStart={() => {}} />)
    await waitFor(() => expect(screen.getByText('Export card')).toBeTruthy())
    expect(screen.queryByText('⧉ Duplicate card')).toBeNull()
  })

  // Duplicating is not destructive, so it must not wear the destructive styling
  // — and it must not be the sheet's primary act either.
  it('is a calm secondary control, not the delete', async () => {
    render(
      <CardSheet client={stub()} card={card} busy={false} onClose={() => {}} onStart={() => {}} onDuplicate={() => {}} onDelete={() => {}} />,
    )
    const btn = await waitFor(() => screen.getByText('⧉ Duplicate card'))
    expect(btn.className).toContain('stage-sheet__duplicate')
    expect(btn.className).not.toContain('stage-sheet__delete')
  })
})
