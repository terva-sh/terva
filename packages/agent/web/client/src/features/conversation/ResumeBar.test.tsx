// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'

import { ResumeBar } from './ResumeBar'

const noop = () => {}

afterEach(cleanup)

describe('ResumeBar', () => {
  // The whole point: a session that died mid-turn shows the user's own message
  // and nothing else, so without this row nothing on screen says a way forward
  // exists.
  it('offers to resume when the daemon says the session is stuck', () => {
    render(<ResumeBar state="after-user" onResume={noop} />)
    expect(screen.getByText(/without a reply/i)).toBeTruthy()
    expect(screen.getByRole('button', { name: /ask again/i })).toBeTruthy()
  })

  // A cut-short reply is a different fact and gets different words: the reply is
  // there, it just stops. "Ask again" would misdescribe what the button does,
  // which is finish the message already on screen.
  it('words a cut-short reply differently from a missing one', () => {
    render(<ResumeBar state="after-cut-short" onResume={noop} />)
    expect(screen.getByText(/stopped partway/i)).toBeTruthy()
    expect(screen.getByRole('button', { name: /finish it/i })).toBeTruthy()
  })

  // A healthy session must render nothing at all. A standing offer that is
  // usually noise teaches people to stop reading the row.
  it('renders nothing when the session is not stuck', () => {
    const { container } = render(<ResumeBar onResume={noop} />)
    expect(container.textContent).toBe('')
  })

  // A turn in flight is itself proof the session is not stuck, and `state` was
  // computed at the last snapshot, so it can be stale for exactly as long as the
  // turn runs.
  it('hides while a turn is running, even when state is set', () => {
    const { container } = render(<ResumeBar state="after-user" busy onResume={noop} />)
    expect(container.textContent).toBe('')
  })

  // Nothing is sent until the user asks. A resume costs tokens, so the component
  // only makes the choice available.
  it('sends nothing until the button is pressed', () => {
    const onResume = vi.fn()
    render(<ResumeBar state="after-user" onResume={onResume} />)
    expect(onResume).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: /ask again/i }))
    expect(onResume).toHaveBeenCalledTimes(1)
  })
})
