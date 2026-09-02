// @vitest-environment happy-dom
//
// The suggested-next-step offer (suggest.next_step) as the composer presents
// it: a strip above the textarea, accepted with Tab or a click and discarded
// with Escape.
//
// The rule these tests exist to hold is precedence. A suggestion is the
// machine's idea and must never displace the user's own words, and it must
// never steal a key that already meant something -- Tab belongs to @-completion
// and to the slash menu first, and only reaches the offer when both decline it.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { Composer } from './Composer'

const props = (
  overrides: Partial<Parameters<typeof Composer>[0]> = {},
): Parameters<typeof Composer>[0] => ({
  busy: false,
  onSend: () => true,
  onToast: () => {},
  commands: [],
  skills: [],
  onCancel: () => {},
  ...overrides,
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const box = () => screen.getByRole('textbox') as HTMLTextAreaElement

// Type into the composer the way a user does, so the component's own onInput
// bookkeeping runs rather than the value being poked in behind its back.
const type = (value: string) => fireEvent.input(box(), { target: { value } })

describe('the suggested next step', () => {
  it('offers nothing at all when the daemon returns an empty line', () => {
    // An empty line is an ordinary answer -- the daemon invites the model to
    // stay quiet when no next step is obvious -- so it must render nothing
    // rather than an empty strip.
    for (const suggestion of [undefined, null, '', '   ']) {
      const { container, unmount } = render(<Composer {...props({ suggestion })} />)
      expect(container.querySelector('.composer-offer')).toBeNull()
      unmount()
    }
  })

  it('shows the offered line', () => {
    render(<Composer {...props({ suggestion: 'run the tests' })} />)
    expect(screen.getByText('run the tests')).toBeTruthy()
  })

  it('puts the line in the composer when it is clicked, and says it was taken', async () => {
    const onAcceptSuggestion = vi.fn()
    render(<Composer {...props({ suggestion: 'run the tests', onAcceptSuggestion })} />)

    fireEvent.click(screen.getByText('run the tests'))

    await waitFor(() => expect(box().value).toBe('run the tests'))
    // Nothing is sent: the host clears the offer, the user still presses send.
    expect(onAcceptSuggestion).toHaveBeenCalledTimes(1)
  })

  it('accepts on Tab and discards on Escape', async () => {
    const onAcceptSuggestion = vi.fn()
    const onDismissSuggestion = vi.fn()
    const { rerender } = render(
      <Composer {...props({ suggestion: 'run the tests', onAcceptSuggestion, onDismissSuggestion })} />,
    )

    fireEvent.keyDown(box(), { key: 'Tab' })
    await waitFor(() => expect(box().value).toBe('run the tests'))
    expect(onAcceptSuggestion).toHaveBeenCalledTimes(1)

    rerender(
      <Composer {...props({ suggestion: 'try again', onAcceptSuggestion, onDismissSuggestion })} />,
    )
    fireEvent.keyDown(box(), { key: 'Escape' })
    expect(onDismissSuggestion).toHaveBeenCalledTimes(1)
  })

  it('never displaces what the user has already written', async () => {
    // The precedence rule, made concrete. The terminal's ghost text loses to a
    // withdrawn prompt and to a stashed draft; here the equivalent is that
    // accepting inserts at the cursor and destroys nothing.
    render(<Composer {...props({ suggestion: 'INSERTED' })} />)

    type('hello world')
    const el = box()
    el.selectionStart = 5
    el.selectionEnd = 5

    fireEvent.keyDown(el, { key: 'Tab' })

    await waitFor(() => expect(box().value).toBe('helloINSERTED world'))
  })

  it('leaves Tab to the slash menu while it is open', async () => {
    // Tab is claimed by the menu first. If the offer took it here, choosing a
    // command with the keyboard would silently paste a suggestion instead.
    const onAcceptSuggestion = vi.fn()
    const onSend = vi.fn(() => true)
    render(
      <Composer
        {...props({
          suggestion: 'run the tests',
          onAcceptSuggestion,
          onSend,
          commands: [{ name: 'compact', desc: 'Summarize', run: () => {} }],
        })}
      />,
    )

    type('/comp')
    fireEvent.keyDown(box(), { key: 'Tab' })

    expect(onAcceptSuggestion).not.toHaveBeenCalled()
    await waitFor(() => expect(onSend).toHaveBeenCalledWith('/compact', [], []))
  })

  it('drops the offer when the message is sent', async () => {
    // Drop, not hide: a suggestion computed against a conversation that has
    // since moved is worse than none, because it still looks current.
    const onDismissSuggestion = vi.fn()
    render(<Composer {...props({ suggestion: 'run the tests', onDismissSuggestion })} />)

    type('something of my own')
    fireEvent.keyDown(box(), { key: 'Enter' })

    await waitFor(() => expect(onDismissSuggestion).toHaveBeenCalled())
  })

  it('reports emptiness upward, because the host cannot see this text', async () => {
    // The idle trigger lives in the host, which owns the timer and the wire but
    // not the composer's text. Without this the unbidden offer would arrive
    // over a half-written message.
    const onEmptyChange = vi.fn()
    render(<Composer {...props({ onEmptyChange })} />)

    await waitFor(() => expect(onEmptyChange).toHaveBeenLastCalledWith(true))

    type('half a thought')
    await waitFor(() => expect(onEmptyChange).toHaveBeenLastCalledWith(false))

    // Whitespace alone is still empty for the purpose of "has not started".
    type('   ')
    await waitFor(() => expect(onEmptyChange).toHaveBeenLastCalledWith(true))
  })
})
