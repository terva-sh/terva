import { describe, expect, it } from 'vitest'
import { REVERTABLE_FIELDS, fieldChanges, fieldLabel } from './carddiff'

describe('fieldChanges', () => {
  const revision = {
    name: 'Kobeni',
    personality: 'anxious',
    tags: ['office', 'cursed'],
    character_book: { entries: [{ keys: ['k'], content: 'c' }] },
  }
  const current = {
    name: 'Kobeni',
    personality: 'steady',
    tags: ['office'],
    character_book: null,
  }

  it('pairs each named field’s value then against now', () => {
    const rows = fieldChanges(revision, current, ['personality', 'tags'])
    expect(rows).toHaveLength(2)
    expect(rows[0]).toMatchObject({
      field: 'personality',
      label: 'Personality',
      before: 'anxious',
      after: 'steady',
      revertable: true,
      value: 'anxious',
    })
    // A string list reads as a list, and the raw value is what a revert hands
    // back — the joined text is for the eye only.
    expect(rows[1]).toMatchObject({ before: 'office, cursed', after: 'office', revertable: true })
    expect(rows[1].value).toEqual(['office', 'cursed'])
  })

  it('takes the server’s field list as given', () => {
    // Not recomputed here: the row count must always agree with the label on
    // the collapsed row, which is built from the same list.
    const rows = fieldChanges(revision, current, ['name'])
    expect(rows).toHaveLength(1)
    expect(rows[0].field).toBe('name')
    // …even though name is identical in both, because the server said so.
    expect(rows[0].before).toBe(rows[0].after)
  })

  it('reports a structural field without pretending to render it', () => {
    const rows = fieldChanges(revision, current, ['character_book'])
    expect(rows[0]).toMatchObject({ field: 'character_book', label: 'Lorebook', revertable: false })
    // A lorebook has no honest one-line rendering, so neither side claims one.
    expect(rows[0].before).toBe('')
    expect(rows[0].after).toBe('')
  })

  it('handles a field absent from one side', () => {
    const rows = fieldChanges({ scenario: 'a bank' }, {}, ['scenario'])
    expect(rows[0].before).toBe('a bank')
    expect(rows[0].after).toBe('')
  })
})

describe('REVERTABLE_FIELDS', () => {
  // The rule this set encodes: the save path writes the form over the stored
  // document, so a field the form does not render must not be revertable on its
  // own — it would change the saved card with nothing on screen to show it.
  it('is exactly the fields the editor renders', () => {
    expect([...REVERTABLE_FIELDS].sort()).toEqual(
      [
        'alternate_greetings',
        'character_version',
        'creator',
        'creator_notes',
        'description',
        'first_mes',
        'mes_example',
        'name',
        'personality',
        'post_history_instructions',
        'scenario',
        'system_prompt',
        'tags',
      ].sort(),
    )
  })

  it('excludes everything the editor cannot show', () => {
    for (const f of ['character_book', 'extensions', 'nickname', 'assets', 'source']) {
      expect(REVERTABLE_FIELDS.has(f)).toBe(false)
    }
  })
})

describe('fieldLabel', () => {
  it('matches the labels the editor puts on its own inputs', () => {
    // Drift here is the bug: the doctor and the history both label a proposal
    // by field name, and until this table was shared they disagreed.
    expect(fieldLabel('first_mes')).toBe('First message')
    expect(fieldLabel('mes_example')).toBe('Example dialogue')
    expect(fieldLabel('character_version')).toBe('Version')
    expect(fieldLabel('post_history_instructions')).toBe('Post-history instructions')
  })

  it('falls back to the raw name for a field it has never heard of', () => {
    expect(fieldLabel('x_future_field')).toBe('x_future_field')
  })
})
