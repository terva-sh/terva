// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { AskRequest } from './AskRequest'
import { PermissionRequest } from './PermissionRequest'

afterEach(cleanup)

describe('PermissionRequest', () => {
  it('emits each existing permission decision', () => {
    const onDecide = vi.fn()
    render(<PermissionRequest request={{ call_id: 'c1', tool: 'bash', preview: 'pwd' }} onDecide={onDecide} />)

    fireEvent.click(screen.getByRole('button', { name: 'Allow' }))
    fireEvent.click(screen.getByRole('button', { name: 'Allow & remember' }))
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))

    expect(onDecide.mock.calls).toEqual([
      ['c1', { allow: true }],
      ['c1', { allow: true, remember_tool: true }],
      ['c1', { allow: false, reason: 'denied by user' }],
    ])
  })

  it('preserves the preview truncation limit', () => {
    render(<PermissionRequest request={{ call_id: 'c1', tool: 'read', preview: 'x'.repeat(1600) }} onDecide={() => {}} />)
    expect(screen.getByText(/…$/).textContent).toHaveLength(1501)
  })

  it('offers the scoped grant only when the daemon derived scopes, echoing the patterns', () => {
    const onDecide = vi.fn()
    render(
      <PermissionRequest
        request={{
          call_id: 'c2',
          tool: 'bash',
          preview: 'git status',
          scopes: [{ display: 'git status', pattern: '^git status(?:\\s|$)' }],
        }}
        onDecide={onDecide}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Always allow “git status”' }))
    expect(onDecide).toHaveBeenCalledWith('c2', {
      allow: true,
      persist_tool: true,
      persist_scopes: ['^git status(?:\\s|$)'],
    })
  })

  it('renders no scoped button without scopes', () => {
    render(<PermissionRequest request={{ call_id: 'c3', tool: 'bash', preview: 'ls' }} onDecide={() => {}} />)
    expect(screen.queryByRole('button', { name: /Always allow/ })).toBeNull()
  })
})

describe('AskRequest', () => {
  it('answers with a presented option', () => {
    const onAnswer = vi.fn()
    render(<AskRequest request={{ ask_id: 'a1', question: 'Choose', options: ['One', 'Two'] }} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: 'Two' }))
    expect(onAnswer).toHaveBeenCalledWith('a1', [{ answer: 'Two' }])
  })

  it('trims and submits a custom answer', () => {
    const onAnswer = vi.fn()
    render(<AskRequest request={{ ask_id: 'a2', question: 'Explain', allow_custom: true }} onAnswer={onAnswer} />)
    fireEvent.input(screen.getByPlaceholderText('custom answer…'), { target: { value: '  details  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('a2', [{ answer: 'details' }])
  })

  // Found by screenshot, not by assertion: a lone question showing only
  // its option buttons was rendering a permanently-disabled Send beneath
  // them with nothing to submit. Clicking an option is the submit.
  it('shows no submit button for a lone question until an input is open', () => {
    render(
      <AskRequest
        request={{ ask_id: 'a6', question: 'Choose', options: ['One', 'Two'], allow_custom: true }}
        onAnswer={() => {}}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Type my own answer…' }))
    expect(screen.getByRole('button', { name: 'Send' })).not.toBeNull()
  })

  // A question with no options is free text whether or not allow_custom is
  // set. Requiring the flag left an optionless ask rendering a card with
  // nothing at all to answer with — and optionless is the tool's own
  // default shape.
  it('offers an input for an optionless question without allow_custom', () => {
    const onAnswer = vi.fn()
    render(<AskRequest request={{ ask_id: 'a3', question: 'Name it?' }} onAnswer={onAnswer} />)
    fireEvent.input(screen.getByPlaceholderText('custom answer…'), { target: { value: 'svc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('a3', [{ answer: 'svc' }])
  })

  // A set is answered in one pass: every question on one card, one submit,
  // answers positional.
  it('answers a question set in one submit', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{
          ask_id: 'a4',
          question: 'Which database?',
          questions: [
            { question: 'Which database?', options: ['Postgres', 'SQLite'] },
            { question: 'Migrate when?', options: ['Now', 'At deploy'] },
            { question: 'Name it?' },
          ],
        }}
        onAnswer={onAnswer}
      />,
    )
    // Picking does NOT send while the rest is unanswered.
    fireEvent.click(screen.getByRole('button', { name: 'SQLite' }))
    expect(onAnswer).not.toHaveBeenCalled()
    expect(
      (screen.getByRole('button', { name: 'Send answers' }) as HTMLButtonElement).disabled,
    ).toBe(true)

    fireEvent.click(screen.getByRole('button', { name: 'At deploy' }))
    fireEvent.input(screen.getByPlaceholderText('custom answer…'), { target: { value: 'billing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send answers' }))
    expect(onAnswer).toHaveBeenCalledWith('a4', [
      { answer: 'SQLite' },
      { answer: 'At deploy' },
      { answer: 'billing' },
    ])
  })

  // Revising before submit is the point of showing the set at once.
  it('sends the revised choice, not the first one', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{
          ask_id: 'a5',
          question: 'One?',
          questions: [
            { question: 'One?', options: ['a', 'b'] },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={onAnswer}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'a' }))
    fireEvent.click(screen.getByRole('button', { name: 'x' }))
    fireEvent.click(screen.getByRole('button', { name: 'b' })) // changed my mind
    fireEvent.click(screen.getByRole('button', { name: 'Send answers' }))
    expect(onAnswer).toHaveBeenCalledWith('a5', [{ answer: 'b' }, { answer: 'x' }])
  })
  // A note is an addendum to a chosen option — "mostly this, but…" — and
  // travels in its own field so the daemon can still tell WHICH option was
  // picked. Folded into the answer string that distinction is gone.
  it('sends a note alongside the chosen option', () => {
    const onAnswer = vi.fn()
    render(<AskRequest request={{ ask_id: 'a6', question: 'Choose', options: ['One', 'Two'] }} onAnswer={onAnswer} />)

    fireEvent.click(screen.getByRole('button', { name: 'Add a note…' }))
    fireEvent.input(screen.getByPlaceholderText(/note on your answer/), {
      target: { value: 'mostly this, but check the limits' },
    })
    // A lone question answers on click UNTIL a note is open: the note has
    // to go with the choice, so clicking an option now only selects it.
    fireEvent.click(screen.getByRole('button', { name: 'Two' }))
    expect(onAnswer).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('a6', [
      { answer: 'Two', note: 'mostly this, but check the limits' },
    ])
  })

  it('omits an empty note rather than sending a blank field', () => {
    const onAnswer = vi.fn()
    render(<AskRequest request={{ ask_id: 'a7', question: 'Choose', options: ['One'] }} onAnswer={onAnswer} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add a note…' }))
    fireEvent.click(screen.getByRole('button', { name: 'One' }))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('a7', [{ answer: 'One' }])
  })

  // Free text IS the user's own words; a note on it would be a second box
  // for the same thing.
  it('offers no note on a free-text question', () => {
    render(<AskRequest request={{ ask_id: 'a8', question: 'Name?' }} onAnswer={() => {}} />)
    expect(screen.queryByRole('button', { name: 'Add a note…' })).toBeNull()
  })

  it('keeps each question\'s note with its own answer in a set', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{
          ask_id: 'a9',
          question: 'One?',
          questions: [
            { question: 'One?', options: ['a', 'b'] },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={onAnswer}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'a' }))
    fireEvent.click(screen.getByRole('button', { name: 'y' }))
    const noteButtons = screen.getAllByRole('button', { name: 'Add a note…' })
    fireEvent.click(noteButtons[1])
    fireEvent.input(screen.getByPlaceholderText(/note on your answer/), {
      target: { value: 'only if the migration lands' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Send answers' }))
    expect(onAnswer).toHaveBeenCalledWith('a9', [
      { answer: 'a' },
      { answer: 'y', note: 'only if the migration lands' },
    ])
  })

  // Multi-select: the options are not mutually exclusive, so ticking one
  // cannot mean "done" and the answer has to carry the whole list.
  it('accumulates ticks and sends the list', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{
          ask_id: 'm1',
          question: 'Which to enable?',
          options: ['redis', 'postgres', 's3'],
          multi_select: true,
        }}
        onAnswer={onAnswer}
      />,
    )
    // A lone single-select question answers on click. A multi-select one
    // must NOT — the next tick is the point of the question.
    fireEvent.click(screen.getByRole('button', { name: /^[☐☑] redis$/ }))
    expect(onAnswer).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: /^[☐☑] s3$/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('m1', [
      { answer: 'redis, s3', answers: ['redis', 's3'] },
    ])
  })

  it('unticks an option that is clicked twice', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{ ask_id: 'm2', question: 'Which?', options: ['a', 'b'], multi_select: true }}
        onAnswer={onAnswer}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /^[☐☑] a$/ }))
    fireEvent.click(screen.getByRole('button', { name: /^[☐☑] b$/ }))
    fireEvent.click(screen.getByRole('button', { name: /^[☐☑] a$/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('m2', [{ answer: 'b', answers: ['b'] }])
  })

  // "None of these" is an ANSWER to "which of these should I enable?", not a
  // refusal to answer. Gating Send on one tick would leave that user only two
  // ways out: agree to something they don't want, or dismiss the card — which
  // declines every other question in the set with it.
  it('lets an empty multi-select be submitted', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{ ask_id: 'm3', question: 'Which?', options: ['a', 'b'], multi_select: true }}
        onAnswer={onAnswer}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('m3', [{ answer: '', answers: [] }])
  })

  // A ticked box and an unticked one must not be announced identically; the
  // checkmark glyph is decoration and a screen reader never sees it.
  it('marks multi-select options as toggles for assistive tech', () => {
    render(
      <AskRequest
        request={{ ask_id: 'm4', question: 'Which?', options: ['a', 'b'], multi_select: true }}
        onAnswer={() => {}}
      />,
    )
    const a = screen.getByRole('button', { name: /^[☐☑] a$/ })
    expect(a.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(a)
    expect(screen.getByRole('button', { name: /^[☐☑] a$/ }).getAttribute('aria-pressed')).toBe('true')
  })

  // allow_custom means "as well as the options" — on a list of choices the
  // user's own item sits beside the offered ones rather than replacing them.
  it('adds a typed entry alongside the ticked ones', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{
          ask_id: 'm5',
          question: 'Which?',
          options: ['a', 'b'],
          multi_select: true,
          allow_custom: true,
        }}
        onAnswer={onAnswer}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /^[☐☑] a$/ }))
    fireEvent.click(screen.getByRole('button', { name: /Add my own/ }))
    fireEvent.input(screen.getByPlaceholderText('custom answer…'), { target: { value: '  c  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onAnswer).toHaveBeenCalledWith('m5', [{ answer: 'a, c', answers: ['a', 'c'] }])
  })

  // Single-select is untouched: it still answers on click and still sends no
  // list, so a daemon reading `answers` never sees one where none was chosen.
  it('leaves single-select answering on click with no list', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest request={{ ask_id: 'm6', question: 'Which?', options: ['a', 'b'] }} onAnswer={onAnswer} />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'b' }))
    expect(onAnswer).toHaveBeenCalledWith('m6', [{ answer: 'b' }])
  })
})
