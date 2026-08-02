import { m } from '../../i18n'

// Turning "this revision differs in personality and first_mes" into rows a
// reader can act on. Pure and separately tested: vitest gates CI, a click in a
// smoke test does not.
//
// WHICH fields differ is the server's answer (cards.history / cards.revision
// carry `fields`), computed over parsed cards so absent-vs-empty and lorebook
// contents are judged once, in one place. This module only renders the pair of
// values and decides what may be reverted individually.

// The fields the editor actually shows. A per-field revert may touch ONLY
// these: the save path spreads the stored document and overwrites it with the
// form, so writing a field the form does not render would change the saved card
// with nothing on screen to say so. Everything else is reported and left to a
// whole-revision restore.
// The synthetic field name the portrait is reported under. It is not part of
// the card document — the picture is a separate file — so it can never be
// revertable on its own and is deliberately absent from REVERTABLE_FIELDS.
export const PORTRAIT_FIELD = 'portrait'

export const REVERTABLE_FIELDS: ReadonlySet<string> = new Set([
  'name',
  'creator',
  'character_version',
  'description',
  'personality',
  'scenario',
  'first_mes',
  'alternate_greetings',
  'mes_example',
  'system_prompt',
  'post_history_instructions',
  'creator_notes',
  'tags',
])

// Marked here, translated at the render site with tr() — a module-level table
// evaluated at import time would freeze the locale picked before the catalog
// arrived.
const FIELD_LABELS: Record<string, string> = {
  name: m('Name'),
  creator: m('Creator'),
  character_version: m('Version'),
  description: m('Description'),
  personality: m('Personality'),
  scenario: m('Scenario'),
  first_mes: m('First message'),
  alternate_greetings: m('Alternate greetings'),
  mes_example: m('Example dialogue'),
  system_prompt: m('System prompt'),
  post_history_instructions: m('Post-history instructions'),
  creator_notes: m('Creator notes'),
  tags: m('Tags'),
  character_book: m('Lorebook'),
  extensions: m('Extensions'),
  nickname: m('Nickname'),
  source: m('Source'),
  assets: m('Assets'),
  group_only_greetings: m('Group-only greetings'),
  creator_notes_multilingual: m('Translated creator notes'),
  creation_date: m('Creation date'),
  modification_date: m('Modification date'),
  // Not a card field: the portrait lives outside the document, and is reported
  // alongside the fields so a picture-only revision is not labelled "identical".
  [PORTRAIT_FIELD]: m('Portrait'),
}

export interface FieldChange {
  /** The CCv2 field name, as the server named it. */
  field: string
  /** English source label — render with tr(), not t(). */
  label: string
  /** The revision's value as text, or '' when it has no one-line rendering. */
  before: string
  /** The card's current value as text. */
  after: string
  /**
   * Whether this one field can be copied into the editor on its own. False for
   * anything the editor does not render — those need a whole-revision restore,
   * which at least shows its effect in the fields that ARE on screen.
   */
  revertable: boolean
  /** The revision's raw value, handed back verbatim on a per-field revert. */
  value: unknown
}

/** fieldLabel is the English source label for a card field, or the field name. */
export function fieldLabel(field: string): string {
  // Indexed alternate greetings (alternate_greetings[0]) are addressed one at a
  // time by the card doctor. Numbered from 1 for the label: the author counting
  // greetings on screen is not counting from zero.
  const g = /^alternate_greetings\[(\d+)\]$/.exec(field)
  if (g) return `${FIELD_LABELS.alternate_greetings} #${Number(g[1]) + 1}`
  return FIELD_LABELS[field] ?? field
}

/**
 * fieldChanges pairs each named field's value in a revision against the card as
 * it currently stands. `fields` is the server's list; nothing is inferred here,
 * so the rows and the row COUNT always agree with the label on the list entry.
 */
export function fieldChanges(
  revision: Record<string, unknown>,
  current: Record<string, unknown>,
  fields: readonly string[],
): FieldChange[] {
  return fields.map((field) => ({
    field,
    label: fieldLabel(field),
    before: asText(revision[field]),
    after: asText(current[field]),
    revertable: REVERTABLE_FIELDS.has(field),
    value: revision[field],
  }))
}

// asText renders a card value for side-by-side reading. Strings pass through;
// string lists join. Anything structural (a lorebook, an extensions object)
// returns '' — a one-line rendering of it would be a lie, and those fields are
// not individually revertable anyway, so the row reports the change without
// pretending to show it.
function asText(v: unknown): string {
  if (typeof v === 'string') return v
  if (Array.isArray(v)) return v.filter((x) => typeof x === 'string').join(', ')
  return ''
}
