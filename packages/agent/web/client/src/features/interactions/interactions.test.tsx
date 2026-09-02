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

    // One question is open at a time, so the rest of the set is reached by
    // its stub. Nothing is hidden — every question is still on the card.
    fireEvent.click(screen.getByRole('button', { name: /Migrate when\?/ }))
    fireEvent.click(screen.getByRole('button', { name: 'At deploy' }))
    fireEvent.click(screen.getByRole('button', { name: /Name it\?/ }))
    fireEvent.input(screen.getByPlaceholderText('custom answer…'), { target: { value: 'billing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send answers' }))
    expect(onAnswer).toHaveBeenCalledWith('a4', [
      { answer: 'SQLite' },
      { answer: 'At deploy' },
      { answer: 'billing' },
    ])
  })

  // The fold is the point of the change: three questions with five options
  // each filled a whole screen and stayed that way after every choice was
  // made. An answered question becomes one line carrying what was chosen.
  it('opens one question at a time and folds the others to a stub', () => {
    render(
      <AskRequest
        request={{
          ask_id: 'a5b',
          question: 'One?',
          questions: [
            { question: 'One?', options: ['a', 'b'] },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={vi.fn()}
      />,
    )
    // The first question is open; the second is a stub, not a wall of
    // options. This is the part a fresh card gets wrong when every
    // question renders expanded.
    expect(screen.getByRole('button', { name: 'b' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'x' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'a' }))
    // The question just answered stays open: revising the last thing you
    // touched must not cost a click to get back to.
    expect(screen.queryByRole('button', { name: 'b' })).toBeTruthy()
    // Moving to the second folds the first — options gone, answer shown.
    fireEvent.click(screen.getByRole('button', { name: /Two\?/ }))
    expect(screen.queryByRole('button', { name: 'b' })).toBeNull()
    const summary = screen.getByRole('button', { name: /One\?/ })
    expect(summary.textContent).toContain('a')
    // And it reopens to exactly the options it had.
    fireEvent.click(summary)
    expect(screen.getByRole('button', { name: 'b' })).toBeTruthy()
  })

  // The note button used to cost a full row per question on first paint,
  // for the rarest action on the card.
  it('offers no note in a set until that question has an answer', () => {
    render(
      <AskRequest
        request={{
          ask_id: 'a5c',
          question: 'One?',
          questions: [
            { question: 'One?', options: ['a', 'b'] },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={vi.fn()}
      />,
    )
    expect(screen.queryAllByRole('button', { name: 'Add a note…' })).toHaveLength(0)
    fireEvent.click(screen.getByRole('button', { name: 'a' }))
    expect(screen.queryAllByRole('button', { name: 'Add a note…' })).toHaveLength(1)
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
    fireEvent.click(screen.getByRole('button', { name: /Two\?/ }))
    fireEvent.click(screen.getByRole('button', { name: 'x' }))
    // With every question answered the card shows its review, so revising
    // means reopening. That is the cost of folding, and it is one click:
    // the summary row is a button and the options come back as they were.
    fireEvent.click(screen.getByRole('button', { name: /One\?/ }))
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
    fireEvent.click(screen.getByRole('button', { name: /Two\?/ }))
    fireEvent.click(screen.getByRole('button', { name: 'y' }))
    // Answering the last question sends the card to its review, where every
    // question is folded — so annotating question two costs one click to
    // reopen it. Notes are the rarest action on the card; the review being
    // the default at that moment is worth more than saving that click.
    fireEvent.click(screen.getByRole('button', { name: /Two\?/ }))
    // Only the question still open offers a note, so there is exactly one
    // note button and it belongs to question two.
    const noteButtons = screen.getAllByRole('button', { name: 'Add a note…' })
    expect(noteButtons).toHaveLength(1)
    fireEvent.click(noteButtons[0])
    fireEvent.input(screen.getByPlaceholderText(/note on your answer/), {
      target: { value: 'only if the migration lands' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Send answers' }))
    expect(onAnswer).toHaveBeenCalledWith('a9', [
      { answer: 'a' },
      { answer: 'y', note: 'only if the migration lands' },
    ])
  })

  // The strip is how a set stays navigable once its questions are stubs:
  // position always, the model's own short name where it gave one, and a
  // tick once the question is settled.
  it('names the questions it can in the strip, and ticks the settled ones', () => {
    render(
      <AskRequest
        request={{
          ask_id: 'a5c',
          question: 'One?',
          questions: [
            { question: 'One?', slug: 'db', options: ['a', 'b'] },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={vi.fn()}
      />,
    )
    // Named where the model named it, a bare number where it did not — a
    // mixed strip still says more than one of numbers alone.
    expect(screen.getByRole('button', { name: /^1 db$/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /^2$/ })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'a' }))
    expect(screen.getByRole('button', { name: /^1 db ✓$/ })).toBeTruthy()
    // A chip is a way in, not just a label.
    fireEvent.click(screen.getByRole('button', { name: /^2$/ }))
    expect(screen.getByRole('button', { name: 'y' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'b' })).toBeNull()
  })

  it('shows the whole set for review once every question is answered', () => {
    render(
      <AskRequest
        request={{
          ask_id: 'a5d',
          question: 'One?',
          questions: [
            { question: 'One?', options: ['a', 'b'] },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'a' }))
    fireEvent.click(screen.getByRole('button', { name: /Two\?/ }))
    fireEvent.click(screen.getByRole('button', { name: 'y' }))
    // Nothing is open: the card has nothing left to ask, so it shows what
    // is about to be sent instead of the last question's options.
    expect(screen.queryByRole('button', { name: 'b' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'x' })).toBeNull()
    expect(screen.getByRole('button', { name: /One\?/ }).textContent).toContain('a')
    expect(screen.getByRole('button', { name: /Two\?/ }).textContent).toContain('y')
    expect(screen.getByRole('button', { name: 'review' }).getAttribute('aria-current')).toBe('true')
  })

  // Skipping cannot be undone — the agent has been told to proceed without
  // you by the time you notice — so it takes two presses, as esc does in
  // the terminal.
  it('arms the skip before it declines the whole set', () => {
    const onAnswer = vi.fn()
    render(
      <AskRequest
        request={{
          ask_id: 'a5e',
          question: 'One?',
          questions: [
            { question: 'One?', options: ['a', 'b'] },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={onAnswer}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Skip all' }))
    expect(onAnswer).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toBeTruthy()
    // Any other move disarms it, so a stray click cannot end the ask.
    fireEvent.click(screen.getByRole('button', { name: 'a' }))
    expect(screen.queryByRole('alert')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Skip all' }))
    fireEvent.click(screen.getByRole('button', { name: 'Skip all' }))
    // A decline is the whole set, one per question: answers come back
    // positionally, so a short reply would misalign them.
    expect(onAnswer).toHaveBeenCalledWith('a5e', [
      { answer: '', declined: true },
      { answer: '', declined: true },
    ])
  })

  it('reads an untouched multi-select in a stub as none of the options', () => {
    render(
      <AskRequest
        request={{
          ask_id: 'a5f',
          question: 'Which?',
          questions: [
            { question: 'Which?', options: ['p', 'q'], multi_select: true },
            { question: 'Two?', options: ['x', 'y'] },
          ],
        }}
        onAnswer={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /^2$/ }))
    // Ticking nothing IS an answer here. Left blank the row reads as a
    // rendering fault, and it is the one a user would most want to catch.
    expect(screen.getByRole('button', { name: /Which\?/ }).textContent).toContain(
      '(none of the options)',
    )
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
