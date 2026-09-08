import { describe, expect, it } from 'vitest'
import { applyDisplay, fillTemplate, isBodyKind, redactedKeys } from './display'

const call = (args: unknown, result = '', error = false) => ({ args, result, error })

describe('fillTemplate', () => {
  it('substitutes top-level argument values', () => {
    expect(fillTemplate('{city}', { city: 'Berlin' })).toBe('Berlin')
    expect(fillTemplate('{action} {title}', { action: 'complete', title: 'ship it' })).toBe(
      'complete ship it',
    )
  })

  it('substitutes numbers and booleans, which are as nameable as strings', () => {
    expect(fillTemplate('{n} {ok}', { n: 42, ok: false })).toBe('42 false')
  })

  it('renders a key the arguments do not carry as empty, and does not throw', () => {
    expect(fillTemplate('{missing}', { city: 'Berlin' })).toBe('')
    expect(fillTemplate('{city}', null)).toBe('')
    expect(fillTemplate('{city}', ['not', 'a', 'bag'])).toBe('')
  })

  // Flattening a nested value into a header says nothing a reader can use, and
  // stringifying one is how a template becomes a way to dump an argument the
  // card meant to mask.
  it('renders an object or array argument as empty rather than stringifying it', () => {
    expect(fillTemplate('{edits}', { edits: [{ oldText: 'secret' }] })).toBe('')
    expect(fillTemplate('{cfg}', { cfg: { token: 'sk-live-1' } })).toBe('')
  })

  it('leaves braces that are not placeholders literal', () => {
    expect(fillTemplate('{ city }', { city: 'Berlin' })).toBe('{ city }')
    expect(fillTemplate('{"a":1}', { a: 'x' })).toBe('{"a":1}')
    expect(fillTemplate('{9lives}', { '9lives': 'x' })).toBe('{9lives}')
  })
})

describe('applyDisplay', () => {
  it('puts the filled template in the subject', () => {
    const p = applyDisplay({ subject: '{city}' }, call({ city: 'Berlin' }))
    expect(p.subject?.text).toBe('Berlin')
  })

  // A hint is never a reason for a card to lose its header: a template that
  // fills to nothing leaves the generic argument-guessing subject in place.
  //
  // "1 argument" is what that generic subject IS for this call, and it is the
  // whole reason the feature exists: stage 1 guesses a subject from a priority
  // list of key names, `city` is not on it, so an extension's tool renders with
  // a header that names nothing. The hint above turns that into "Berlin".
  it('falls back to the generic subject when the template fills to nothing', () => {
    const p = applyDisplay({ subject: '{nope}' }, call({ city: 'Berlin' }))
    expect(p.subject?.text).toBe('1 argument')
  })

  it('leaves the body alone for a subject-only hint', () => {
    const p = applyDisplay({ subject: '{city}' }, call({ city: 'Berlin' }, 'sunny'))
    expect(p.body).toBeUndefined()
  })

  it('renders one body node per line for diff, json and table', () => {
    const diff = applyDisplay({ body: 'diff' }, call({}, '+a\n-b\n c'))
    expect(diff.body).toHaveLength(3)

    const json = applyDisplay({ body: 'json' }, call({}, '{"a":1}'))
    expect(json.body).toEqual(['{', '  "a": 1', '}'])

    const table = applyDisplay({ body: 'table' }, call({}, 'a\t1\nbbbb\t2'))
    expect(table.body).toEqual(['a     1', 'bbbb  2'])
  })

  // Both fallbacks matter: an extension that declared a body and returned
  // something else should still show what it returned.
  it('falls back to plain lines when the result does not fit the declared body', () => {
    const json = applyDisplay({ body: 'json' }, call({}, 'Traceback:\n  boom'))
    expect(json.body).toEqual(['Traceback:', '  boom'])

    const table = applyDisplay({ body: 'table' }, call({}, 'no tabs here\njust prose'))
    expect(table.body).toEqual(['no tabs here', 'just prose'])
  })

  // The daemon clears an unknown body before serving it, so this is the case
  // where a NEWER daemon knows a renderer this client does not.
  it('renders a body name it does not know as the plain result', () => {
    const p = applyDisplay({ body: 'hologram' }, call({}, 'x\ny'))
    expect(p.body).toBeUndefined()
  })

  it('never throws on a hint whose fields are all empty', () => {
    expect(() => applyDisplay({}, call(null, ''))).not.toThrow()
  })
})

describe('the closed body set', () => {
  it('admits what the client draws', () => {
    for (const ok of ['text', 'json', 'diff', 'table']) expect(isBodyKind(ok)).toBe(true)
  })

  // "code" was in the proposal and is deliberately not implemented: the body is
  // already monospace with pre-wrap, so it would be a second name for text.
  it('rejects code, and anything else', () => {
    for (const bad of ['code', 'markdown', 'html', 'Table', '']) expect(isBodyKind(bad)).toBe(false)
  })
})

describe('redactedKeys', () => {
  it('lowercases, so a hint naming api_key covers an argument named API_KEY', () => {
    expect(redactedKeys({ redact: ['API_KEY', 'Session'] })).toEqual(new Set(['api_key', 'session']))
  })

  it('is empty for a hint that names none, and for no hint at all', () => {
    expect(redactedKeys({ subject: '{x}' }).size).toBe(0)
    expect(redactedKeys(undefined).size).toBe(0)
  })
})
