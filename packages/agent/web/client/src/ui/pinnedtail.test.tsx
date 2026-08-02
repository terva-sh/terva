// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, waitFor } from '@testing-library/preact'
import { readdirSync, readFileSync } from 'node:fs'
import { relative, resolve } from 'node:path'
import { usePinnedTail } from './pinnedtail'

let scrollHeight = 600
let clientHeight = 300
let originalScrollHeight: PropertyDescriptor | undefined
let originalClientHeight: PropertyDescriptor | undefined

beforeEach(() => {
  scrollHeight = 600
  clientHeight = 300
  originalScrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight')
  originalClientHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientHeight')
  Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => scrollHeight })
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => clientHeight })
})

afterEach(() => {
  cleanup()
  if (originalScrollHeight) Object.defineProperty(HTMLElement.prototype, 'scrollHeight', originalScrollHeight)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).scrollHeight
  if (originalClientHeight) Object.defineProperty(HTMLElement.prototype, 'clientHeight', originalClientHeight)
  else delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientHeight
})

// A minimal host with the same wiring the three real callers use.
function Feed({ lines, feed }: { lines: number; feed?: string }) {
  const { ref, onScroll, showJump, jumpToLatest } = usePinnedTail<HTMLDivElement>([lines, feed], feed)
  return (
    <div>
      <div class="box" ref={ref} onScroll={onScroll}>
        {Array.from({ length: lines }, (_, i) => (
          <div key={i}>line {i}</div>
        ))}
      </div>
      {showJump && (
        <button type="button" onClick={jumpToLatest}>
          jump
        </button>
      )}
    </div>
  )
}

const boxOf = (c: Element) => c.querySelector('.box') as HTMLDivElement

describe('usePinnedTail', () => {
  it('lands at the end on mount and follows new content', async () => {
    const view = render(<Feed lines={1} />)
    const box = boxOf(view.container)
    await waitFor(() => expect(box.scrollTop).toBe(600))

    scrollHeight = 900
    view.rerender(<Feed lines={2} />)
    await waitFor(() => expect(box.scrollTop).toBe(900))
  })

  it('stops following once the reader scrolls up, and resumes after a jump', async () => {
    const view = render(<Feed lines={1} />)
    const box = boxOf(view.container)
    await waitFor(() => expect(box.scrollTop).toBe(600))

    box.scrollTop = 100
    fireEvent.scroll(box)
    expect(view.container.querySelector('button')).toBeTruthy()

    // The whole point: new content arrives and the view does NOT move.
    scrollHeight = 1200
    view.rerender(<Feed lines={5} />)
    expect(box.scrollTop).toBe(100)

    fireEvent.click(view.container.querySelector('button') as HTMLButtonElement)
    expect(box.scrollTop).toBe(1200)
    expect(view.container.querySelector('button')).toBeNull()

    scrollHeight = 1400
    view.rerender(<Feed lines={6} />)
    await waitFor(() => expect(box.scrollTop).toBe(1400))
  })

  it('re-pins without a jump once the reader scrolls back to the end', async () => {
    const view = render(<Feed lines={1} />)
    const box = boxOf(view.container)
    await waitFor(() => expect(box.scrollTop).toBe(600))

    box.scrollTop = 0
    fireEvent.scroll(box)
    scrollHeight = 800
    view.rerender(<Feed lines={2} />)
    expect(box.scrollTop).toBe(0)

    box.scrollTop = 500 // 800 - 500 - 300 = 0 from the bottom
    fireEvent.scroll(box)
    scrollHeight = 1000
    view.rerender(<Feed lines={3} />)
    await waitFor(() => expect(box.scrollTop).toBe(1000))
  })

  it('re-pins when the feed itself changes, even mid-scroll', async () => {
    const view = render(<Feed lines={1} feed="a" />)
    const box = boxOf(view.container)
    await waitFor(() => expect(box.scrollTop).toBe(600))

    box.scrollTop = 0
    fireEvent.scroll(box)
    expect(view.container.querySelector('button')).toBeTruthy()

    // Another session/deliberation: start at ITS end regardless of where the
    // reader was in the previous one.
    scrollHeight = 700
    view.rerender(<Feed lines={1} feed="b" />)
    await waitFor(() => expect(box.scrollTop).toBe(700))
    expect(view.container.querySelector('button')).toBeNull()
  })

  it('scales the pin slack to a short pane instead of calling every position the end', async () => {
    // The RAATI ticker is 74px tall. A flat 80px threshold would read every
    // scroll position as "at the end" and the pin check would never engage.
    clientHeight = 74
    scrollHeight = 400
    const view = render(<Feed lines={1} />)
    const box = boxOf(view.container)
    await waitFor(() => expect(box.scrollTop).toBe(400))

    // 40px up from the bottom — a couple of ticker lines — must unpin. The
    // slack here is min(80, 74/2) = 37; under a flat 80px this would still
    // read as "at the end" and the next event would yank the view back.
    box.scrollTop = 400 - 74 - 40
    fireEvent.scroll(box)
    scrollHeight = 500
    view.rerender(<Feed lines={2} />)
    expect(box.scrollTop).toBe(286)
  })

  it('keeps the 80px threshold on a tall pane', async () => {
    const view = render(<Feed lines={1} />)
    const box = boxOf(view.container)
    await waitFor(() => expect(box.scrollTop).toBe(600))

    box.scrollTop = 221 // 600 - 221 - 300 = 79 from the bottom: still pinned
    fireEvent.scroll(box)
    expect(view.container.querySelector('button')).toBeNull()

    box.scrollTop = 220 // exactly 80: unpinned
    fireEvent.scroll(box)
    expect(view.container.querySelector('button')).toBeTruthy()
  })
})

// --- census -----------------------------------------------------------------
//
// Following a feed's tail is `el.scrollTop = el.scrollHeight`, which reads like
// a complete thought and is wrong without a pin check — that is how the RAATI
// ticker shipped yanking the view back on every event while two other surfaces
// carried the correct version. So the assignment lives in exactly one place and
// this scans for a fourth copy rather than listing the three that existed.

const SRC = resolve(__dirname, '..')

// Modules allowed to write scrollTop directly, and why.
const EXEMPT = new Set([
  'ui/pinnedtail.ts', // the implementation
  // A one-shot anchor, not a feed: the reply sheet opens a STATIC quote at its
  // end (you are answering the last thing said) and re-anchors only when the
  // quoted text itself changes. There is no stream to unpin from, and pinning
  // would be the wrong model — it greps like this bug and is not it.
  'apps/stage/SuggestReply.tsx',
])

function sources(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = resolve(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === 'node_modules') continue
      out.push(...sources(p))
    } else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(p)
    }
  }
  return out
}

// Any assignment to a .scrollTop whose value mentions scrollHeight — the
// follow-the-tail move, however the element is named.
const TAIL_FOLLOW = /\.scrollTop\s*=\s*[^\n;]*scrollHeight/

function rollsItsOwnTailFollow(text: string): boolean {
  return TAIL_FOLLOW.test(text)
}

describe('tail-following census', () => {
  it('no module scrolls a feed to its end without the shared pin check', () => {
    const offenders = sources(SRC)
      .filter((f) => rollsItsOwnTailFollow(readFileSync(f, 'utf8')))
      .map((f) => relative(SRC, f).replaceAll('\\', '/'))
      .filter((rel) => !EXEMPT.has(rel))
    expect(offenders, 'use usePinnedTail (ui/pinnedtail) — a bare scrollTop = scrollHeight steals the scroll from a reader').toEqual([])
  })

  // Teeth: the regexp above must actually catch the shapes it exists for. A
  // detector that has quietly stopped matching passes the census vacuously.
  it('catches the shapes it exists to catch', () => {
    expect(rollsItsOwnTailFollow('if (el) el.scrollTop = el.scrollHeight')).toBe(true)
    expect(rollsItsOwnTailFollow('element.scrollTop = element.scrollHeight')).toBe(true)
    expect(rollsItsOwnTailFollow('ref.current.scrollTop = ref.current.scrollHeight - 0')).toBe(true)
    expect(rollsItsOwnTailFollow('box.scrollTop = 0')).toBe(false)
    expect(rollsItsOwnTailFollow('const h = el.scrollHeight')).toBe(false)
  })
})
