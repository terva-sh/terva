// @vitest-environment happy-dom
//
// The suggest sheet is a bottom sheet over a dimmed backdrop: while you draft a
// reply, the message you are replying to is both covered and greyed out. It is
// quoted inside the sheet so it is readable — and copyable — while you write.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { fakeClient } from '../../platform/ctrlproto/testing'
import type { Verb } from '../../platform/ctrlproto/types'
import type { Item } from '../../platform/conversation/store'
import { replyTarget } from './Chat'
import { SuggestReply } from './SuggestReply'

afterEach(cleanup)

const stub = () =>
  fakeClient({
    respond: (method: Verb) => (method === 'cards.list' ? { cards: [] } : {}),
  })

function open(replyTo?: { actor?: string; text: string }, onClose = vi.fn()) {
  const client = stub()
  const r = render(
    <SuggestReply client={client} sessionId="s1" replyTo={replyTo} onClose={onClose} onUse={vi.fn()} />,
  )
  return { client, onClose, ...r }
}

describe('SuggestReply quotes the line being answered', () => {
  it('shows the message, attributed to whoever spoke it', () => {
    open({ actor: 'Kobeni', text: 'She sets the glass down, *carefully*.' })

    expect(screen.getByText('Replying to Kobeni')).toBeTruthy()
    const quote = document.querySelector('.stage-suggest__quote-body')
    expect(quote).toBeTruthy()
    // Rendered as the scene renders it — the emphasis is real markup, so what
    // you select and copy is the message as you saw it, not its source.
    expect(quote?.querySelector('em')?.textContent).toBe('carefully')
    expect(quote?.textContent).toContain('She sets the glass down')
  })

  it('is not an editor', () => {
    open({ actor: 'Kobeni', text: 'a line' })
    const quote = document.querySelector('.stage-suggest__quote-body') as HTMLElement
    // No textarea, no input, nothing contenteditable: there is nothing here to
    // edit, and an editor would invite editing the transcript from the wrong
    // place. The sheet's own composer is elsewhere.
    expect(quote.querySelector('textarea')).toBeNull()
    expect(quote.querySelector('input')).toBeNull()
    expect(quote.querySelector('[contenteditable]')).toBeNull()
  })

  it('says nothing when there is no line to answer', () => {
    open(undefined)
    expect(document.querySelector('.stage-suggest__quote')).toBeNull()
    // A scene whose newest message is empty (a cancelled turn) is the same case.
    cleanup()
    open({ actor: 'Kobeni', text: '   ' })
    expect(document.querySelector('.stage-suggest__quote')).toBeNull()
  })

  it('falls back to an unattributed label', () => {
    open({ text: 'a line with no actor' })
    expect(screen.getByText('Replying to')).toBeTruthy()
  })
})

describe('SuggestReply backdrop', () => {
  it('still closes on a plain click outside', () => {
    const { onClose } = open({ actor: 'Kobeni', text: 'a line' })
    fireEvent.click(document.querySelector('.stage-sheet-backdrop') as HTMLElement)
    expect(onClose).toHaveBeenCalled()
  })

  it('does not close when the press began inside the sheet', () => {
    const onClose = vi.fn()
    open({ actor: 'Kobeni', text: 'a line' }, onClose)
    const backdrop = document.querySelector('.stage-sheet-backdrop') as HTMLElement
    const quote = document.querySelector('.stage-suggest__quote-body') as HTMLElement

    // A selection drag that starts on the quote and ends outside delivers its
    // click to the BACKDROP — press and release have no closer common ancestor
    // — so the sheet's stopPropagation never sees it. Dismissing there would
    // drop the selection mid-copy, which is what the quote exists to allow.
    fireEvent.pointerDown(quote)
    fireEvent.click(backdrop)
    expect(onClose).not.toHaveBeenCalled()

    // …and the very next deliberate click still closes. Keying on the live
    // selection instead would swallow this one too — the browser does not
    // collapse the selection before the click dispatches — leaving the sheet
    // needing two taps to dismiss.
    fireEvent.pointerDown(backdrop)
    fireEvent.click(backdrop)
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

// A source rule, because vitest gates CI and a real browser does not run here.
// The cap is the whole of the second half of the request: a long scene beat must
// scroll inside the quote instead of pushing the sketch box off the screen.
describe('the quote is bounded and scrolls', () => {
  const css = readFileSync(resolve(__dirname, 'stage.css'), 'utf8')

  const ruleBody = (selector: string) => {
    const i = css.indexOf(selector + ' {')
    expect(i, `${selector} missing from stage.css`).toBeGreaterThan(-1)
    return css.slice(i, css.indexOf('}', i))
  }

  it('caps its height against the viewport and scrolls the overflow', () => {
    const body = ruleBody('.stage-suggest__quote-body')
    expect(body).toMatch(/max-height:\s*\d+vh/)
    expect(body).toMatch(/overflow-y:\s*auto/)
    // Without min-height:0 a flex item refuses to shrink below its content, so
    // the cap loses and the box grows towards the top of the screen anyway.
    expect(body).toMatch(/min-height:\s*0/)
    expect(ruleBody('.stage-suggest__quote')).toMatch(/min-height:\s*0/)
  })

  it('keeps the quoted bubble selectable and not typable', () => {
    const rule = ruleBody('.stage-suggest__quote-body .stage-bubble')
    expect(rule).toMatch(/user-select:\s*text/)
    // .stage-bubble sets cursor:text, which in the scene invites tap-to-edit.
    // Here there is nothing to edit, so the invitation is withdrawn.
    expect(rule).toMatch(/cursor:\s*auto/)
  })
})

describe('replyTarget', () => {
  const asst = (id: string, text: string, actor?: string) =>
    ({ kind: 'assistant', id, text, streaming: false, actor }) as Item
  const user = (id: string, text: string) => ({ kind: 'user', id, text }) as Item
  const notice = (id: string) => ({ kind: 'notice', id, level: 'info', text: 'n' }) as Item

  it('picks the newest spoken line, not the last item', () => {
    // The tail is routinely something nobody said. Taking items.at(-1) would
    // quote a notice, or quote the user back at themselves.
    const items = [asst('a', 'the line being answered'), user('u', 'my half-typed reply'), notice('n')]
    expect(replyTarget(items, 'Kobeni')).toEqual({ actor: 'Kobeni', text: 'the line being answered' })
  })

  it('prefers an attributed actor over the bound character', () => {
    // A posted Character or Narrator beat names its own speaker; only an
    // ordinary reply is the card the session is bound to.
    const items = [asst('a', 'ordinary reply'), asst('b', 'Kael shoulders the door open.', 'Kael')]
    expect(replyTarget(items, 'Kobeni')).toEqual({ actor: 'Kael', text: 'Kael shoulders the door open.' })
  })

  it('skips an empty or streaming-but-silent tail', () => {
    const items = [asst('a', 'the real line'), asst('b', '   ')]
    expect(replyTarget(items, 'Kobeni')?.text).toBe('the real line')
  })

  it('has nothing to quote in a fresh scene', () => {
    expect(replyTarget([], 'Kobeni')).toBeUndefined()
    expect(replyTarget([user('u', 'hello')], 'Kobeni')).toBeUndefined()
  })

  it('copes with an unbound character', () => {
    expect(replyTarget([asst('a', 'x')], undefined)).toEqual({ actor: '', text: 'x' })
  })
})
