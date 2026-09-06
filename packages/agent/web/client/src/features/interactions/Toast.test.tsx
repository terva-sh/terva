// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { act, cleanup, fireEvent, render } from '@testing-library/preact'
import { useToast, ToastDock, TOAST_FADE_MS, TOAST_MS, type Notify } from './Toast'

// A harness that exposes the notify handle, so each test drives the real hook
// rather than a hand-built Toast object. The lifetime rules live in the hook
// and the view together; testing the view alone would let the hook drift.
let notify!: Notify

function Harness() {
  const { toast, notify: n } = useToast()
  notify = n
  return <ToastDock toast={toast} onDismiss={n.clear} />
}

const shown = () => document.querySelector('.toast')
const text = () => shown()?.textContent ?? null

// Advancing the clock runs effects, so every advance is an act().
const tick = async (ms: number) => {
  await act(async () => {
    vi.advanceTimersByTime(ms)
  })
}

const raise = async (fn: () => void) => {
  await act(async () => {
    fn()
  })
}

describe('toast lifetime', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('takes an error down only when it is dismissed', async () => {
    render(<Harness />)
    await raise(() => notify.error('the daemon refused the connection'))
    expect(text()).toBe('the daemon refused the connection')

    // Ten times the timeout. An error has no clock at all — this is not a long
    // wait, it is the absence of one.
    await tick(TOAST_MS * 10)

    // Assert on is-leaving, NOT merely on the text still being there. A toast
    // that has begun to fade is still mounted for one more tick, so a presence
    // check alone passes even when the error HAS expired — which is exactly what
    // happened: this test was written asserting only the text, and it went on
    // passing with the `kind === 'error'` guard deleted. It proved nothing.
    expect(shown()?.classList.contains('is-leaving'), 'an error must never start fading').toBe(false)
    await tick(TOAST_FADE_MS * 4)
    expect(text()).toBe('the daemon refused the connection')

    fireEvent.click(shown() as Element)
    await tick(0)
    expect(shown()).toBeNull()
  })

  it('fades a note out on its own', async () => {
    render(<Harness />)
    await raise(() => notify.note('the draft is kept for this session; attachments are not'))

    // Still there a moment before the timeout — the guard against a timeout so
    // short that the message is gone before it is read.
    await tick(TOAST_MS - 100)
    expect(shown()).not.toBeNull()
    expect(shown()?.classList.contains('is-leaving')).toBe(false)

    // The fade starts on time, and the element stays mounted through it: an
    // unmount at the timeout would make the fade a lie.
    await tick(200)
    expect(shown()?.classList.contains('is-leaving')).toBe(true)

    await tick(TOAST_FADE_MS)
    expect(shown()).toBeNull()
  })

  it('fades a success out on its own', async () => {
    render(<Harness />)
    await raise(() => notify.ok('saved settings for anthropic/claude'))
    // Two steps, not one: the fade timer is armed by the render that applies
    // is-leaving, so it does not exist yet while the first advance is running.
    await tick(TOAST_MS)
    await tick(TOAST_FADE_MS)
    expect(shown()).toBeNull()
  })

  it('holds the clock while the pointer is on it, and resumes with what was left', async () => {
    render(<Harness />)
    await raise(() => notify.note('a long sentence someone is halfway through reading'))

    await tick(TOAST_MS - 5_000) // 5s left
    fireEvent.mouseEnter(shown() as Element)

    // A minute under the pointer. Nothing expires while it is being read.
    await tick(60_000)
    expect(shown()).not.toBeNull()
    expect(shown()?.classList.contains('is-leaving')).toBe(false)

    fireEvent.mouseLeave(shown() as Element)

    // RESUMES rather than restarts. Four of the five remaining seconds pass and
    // it is still up; a restart would leave a full fifteen on the clock here and
    // this assertion would pass for the wrong reason — so the next one, at 5s
    // total, is the half that pins it down.
    await tick(4_000)
    expect(shown()?.classList.contains('is-leaving')).toBe(false)

    await tick(1_500)
    expect(shown()?.classList.contains('is-leaving')).toBe(true)
  })

  it('holds the clock while it has focus', async () => {
    render(<Harness />)
    await raise(() => notify.note('reachable by keyboard too'))
    fireEvent.focus(shown() as Element)
    await tick(TOAST_MS * 2)
    expect(shown()).not.toBeNull()

    fireEvent.blur(shown() as Element)
    await tick(TOAST_MS)
    await tick(TOAST_FADE_MS)
    expect(shown()).toBeNull()
  })

  it('gives a repeated message a fresh clock rather than inheriting the old one', async () => {
    render(<Harness />)
    await raise(() => notify.note('Finish the current turn first.'))
    await tick(TOAST_MS - 1_000)

    // The same words again, one second before the first one would have gone.
    // Keyed on the sequence number, so this is a new toast; without that key the
    // view keeps its state and the message vanishes a second after it appears.
    await raise(() => notify.note('Finish the current turn first.'))
    await tick(2_000)
    expect(shown()).not.toBeNull()
    expect(shown()?.classList.contains('is-leaving')).toBe(false)
  })

  it('clears a progress note only while it is still the one showing', async () => {
    render(<Harness />)
    await raise(() => notify.note('Compacting…'))
    await raise(() => notify.error('the compaction failed'))

    // The ack for the compaction lands after the failure has replaced it.
    // Clearing blindly here would swallow the error the user needs.
    await raise(() => notify.clearIf('Compacting…'))
    expect(text()).toBe('the compaction failed')

    await raise(() => notify.clearIf('the compaction failed'))
    expect(shown()).toBeNull()
  })

  it('treats an empty message as a clear, not as a blank box', async () => {
    render(<Harness />)
    await raise(() => notify.error('something'))
    await raise(() => notify.error(''))
    expect(shown()).toBeNull()
  })
})

describe('toast markup', () => {
  afterEach(cleanup)

  it('keeps the live region mounted while there is nothing to say', () => {
    // A live region inserted at the same moment as its content is announced
    // unreliably. The dock is always there; only its contents change.
    render(<ToastDock toast={null} onDismiss={() => {}} />)
    const dock = document.querySelector('.toast-dock')
    expect(dock).not.toBeNull()
    expect(dock?.getAttribute('role')).toBe('status')
    expect(dock?.getAttribute('aria-live')).toBe('polite')
  })

  it('carries the kind in the class, so the colour is not one red box for everything', () => {
    for (const kind of ['error', 'success', 'note'] as const) {
      cleanup()
      render(<ToastDock toast={{ text: 'x', kind, seq: 1 }} onDismiss={() => {}} />)
      expect(document.querySelector('.toast')?.className).toContain(`toast--${kind}`)
    }
  })

  it('is a button, so it can be dismissed from the keyboard', () => {
    render(<ToastDock toast={{ text: 'x', kind: 'note', seq: 1 }} onDismiss={() => {}} />)
    const el = document.querySelector('.toast') as HTMLButtonElement
    expect(el.tagName).toBe('BUTTON')
    expect(el.type).toBe('button')
  })
})

// The placement fix is a CSS rule paired with a variable the composer writes.
// Neither half can be observed in happy-dom, which does no layout — so this
// reads the stylesheet source and asserts the SHAPE of the rule. The pixel
// truth is tests/smoke/toast-placement.smoke.ts, under a real browser.
describe('toast placement (stylesheet source)', () => {
  const css = readFileSync(resolve(__dirname, '../../styles.css'), 'utf8')
  const dock = /\.toast-dock\s*\{([^}]*)\}/.exec(css)?.[1] ?? ''
  const toast = /^\.toast\s*\{([^}]*)\}/m.exec(css)?.[1] ?? ''

  it('offsets the dock by the composer height rather than a constant', () => {
    expect(dock, 'no .toast-dock rule in styles.css').not.toBe('')
    expect(dock).toContain('--toast-lift')

    // The bug, stated as a rule: the old declaration was `bottom: 80px`, a
    // guess at the composer's height that a multi-line draft or a chip row
    // outgrew. The offset must be derived from the measured height. (A literal
    // term is still fine INSIDE the calc — the 12px there is the gap between
    // the toast and the composer, which is a real constant.)
    const bottom = (/bottom:\s*([^;]+);/.exec(dock)?.[1] ?? '').trim()
    expect(bottom).toContain('var(--toast-lift')
    expect(bottom, 'a constant offset is the old bug').not.toMatch(/^\d+px$/)
  })

  it('falls back to the safe-area inset where there is no composer', () => {
    // Landing and board have no composer, so the variable is absent there. The
    // fallback has to be the inset and not 0, or the toast sits under the home
    // indicator on a phone.
    expect(dock).toContain('var(--toast-lift, var(--safe-bottom))')
  })

  it('does not let the empty dock swallow clicks', () => {
    expect(dock).toMatch(/pointer-events:\s*none/)
    expect(toast).toMatch(/pointer-events:\s*auto/)
  })

  it('fades for exactly as long as the view waits before unmounting', () => {
    // Two numbers in two files that have to agree. Too short a timer clips the
    // fade; too long leaves a spent element sitting over the composer.
    const ms = /transition:[^;]*?(\d+)ms/.exec(toast)?.[1]
    expect(ms, 'no transition duration on .toast').toBeDefined()
    expect(Number(ms)).toBe(TOAST_FADE_MS)
  })
})
