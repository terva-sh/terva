import { useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import { askQuestions, type AskRequest as AskRequestData } from '../../platform/ctrlproto/types'

// One mid-turn ask. A set of questions is stacked in a single card with
// one submit, so the whole set costs the user one interruption.
//
// The set shows ONE question at a time, the way the TUI's dialog does:
// the others are one-line stubs above and below it, and clicking a stub
// moves to it. A set of eight questions with five options each rendered
// expanded is a page of buttons with the send control off the bottom of
// it, which is a form to fill in rather than a question to answer.
// Nothing is hidden — every question is on the card, and its answer is on
// its stub — but only the one being answered is open.
//
// A question with no options is free text whether or not allow_custom is
// set: allow_custom means "as well as the options", and requiring it for
// an optionless question left the card with nothing to answer with at all.
export function AskRequest({
  request,
  onAnswer,
}: {
  request: AskRequestData
  onAnswer: (
    id: string,
    answers: { answer: string; answers?: string[]; note?: string; declined?: boolean }[],
  ) => void
}) {
  const questions = askQuestions(request)
  // A lone question keeps its original one-click answer: there is nothing
  // else on the card to coordinate with, so making the user press Send
  // after picking would be pure friction. A set has to be submitted —
  // same split the TUI makes between one question and a tabbed set.
  //
  // Except once a note is being written: the note is only worth having if
  // it goes WITH the choice, so a lone question grows a Send the moment
  // there is something to send alongside the option.
  const single = questions.length === 1
  const [picked, setPicked] = useState<Record<number, string>>({})
  // Multi-select keeps its own state rather than overloading `picked`: the
  // two answer different questions ("which one" vs "which ones") and a
  // single string cannot hold an empty selection distinctly from no
  // selection, which is the difference between a decline and a choice.
  const [ticked, setTicked] = useState<Record<number, string[]>>({})
  const [custom, setCustom] = useState<Record<number, string>>({})
  const [typing, setTyping] = useState<Record<number, boolean>>({})
  // A note is an addendum to a CHOSEN option, so it is keyed by question
  // and only sent when that question's answer is an option — the same
  // rule the TUI applies by binding the note to the option it was
  // written against.
  const [note, setNote] = useState<Record<number, string>>({})
  const [noting, setNoting] = useState<Record<number, boolean>>({})
  // Which question is open, or the review. This is the card's whole layout
  // model: exactly one of them is expanded and the rest are stubs, so
  // "fold an answered question", "show one at a time" and "review before
  // sending" are one mechanism rather than three interacting ones.
  //
  // It never advances by itself when a question is answered. Answering and
  // then changing your mind is the commonest correction on this card, and
  // auto-advancing would move the options out from under the cursor at
  // exactly that moment. The TUI does not auto-advance either; you press
  // tab. Here you click the next stub, or its chip.
  const [tab, setTab] = useState<number | 'review'>(0)
  // Visited is what makes a ✓ honest for multi-select, where ticking
  // nothing is a real answer ("none of these") and so cannot be told from
  // an untouched question by its selection alone. Question 0 starts
  // visited because it is the one on screen.
  const [visited, setVisited] = useState<Record<number, boolean>>({ 0: true })
  // Skipping is the one action here that cannot be undone — the agent has
  // already been told to proceed without you by the time you notice — so
  // it arms on the first press and fires on the second, as esc does in the
  // TUI. Any other interaction disarms it.
  const [skipArmed, setSkipArmed] = useState(false)

  const isMulti = (i: number) => !!questions[i].multi_select && !!questions[i].options?.length

  // A question shows a text input when it has no options at all, or when
  // the user reached for "type my own answer". Multi-select shows BOTH: the
  // typed entry is one more value alongside the ticked ones, because
  // allow_custom means "as well as the options" and a list of choices is
  // exactly the case where the user's own item can sit beside the offered
  // ones rather than replacing them.
  const isFree = (i: number) => !questions[i].options?.length || (!!typing[i] && !isMulti(i))
  const showsCustomInput = (i: number) => isFree(i) || (isMulti(i) && !!typing[i])

  // chosenFor is what the user picked, in list form, for every shape — the
  // component's single answer to "what have they said so far".
  const chosenFor = (i: number): string[] => {
    if (isMulti(i)) {
      const typed = (custom[i] ?? '').trim()
      const marks = ticked[i] ?? []
      return typing[i] && typed ? [...marks, typed] : marks
    }
    const one = isFree(i) ? (custom[i] ?? '').trim() : (picked[i] ?? '')
    return one ? [one] : []
  }

  const answerFor = (i: number): string => chosenFor(i).join(', ')

  // A note belongs to a chosen option, so free text carries none — that
  // text IS the answer already.
  const noteFor = (i: number): string => (isFree(i) ? '' : (note[i] ?? '').trim())

  // Settled is "this question has been dealt with", which is not the same
  // as "has text in it": an empty multi-select that the user has looked at
  // is settled, and a single-choice question is not settled until
  // something is chosen.
  const settled = (i: number) => (isMulti(i) ? !!visited[i] : answerFor(i) !== '')
  const allSettled = questions.every((_, i) => settled(i))

  // Everything except the open one is a stub. A lone question never folds:
  // it has no siblings to make room for, and it answers on click.
  const folded = (i: number) => !single && tab !== i

  const goto = (i: number | 'review') => {
    setSkipArmed(false)
    setTab(i)
    if (typeof i === 'number') setVisited({ ...visited, [i]: true })
  }

  // Once every question is settled the card has nothing left to ask, so it
  // shows the whole set with its answers and the send control — the TUI's
  // review tab. It does not steal the view mid-sentence: a question with
  // an open text field keeps it, and the review chip is there to reach.
  useEffect(() => {
    if (single || !allSettled) return
    if (typeof tab === 'number' && (showsCustomInput(tab) || noting[tab])) return
    setTab('review')
    // Fires on the transition into "all settled", not on every render that
    // happens to be settled — otherwise reopening a question to revise it
    // would be undone immediately.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [allSettled])

  // A lone question that is showing only its option buttons needs no
  // submit — clicking an option sends. Rendering one anyway left a dead,
  // permanently-disabled Send under the options with nothing to submit.
  // A note changes that: the option can no longer send on click, because
  // the note has to go with it.
  //
  // Multi-select always needs one: ticking a box cannot mean "done" when
  // the next box is the point of the question.
  const showSubmit = !single || isFree(0) || !!noting[0] || isMulti(0)

  // Every question needs something to send. A choice defaults to nothing
  // picked rather than to the first option: on a form the user can see
  // all at once, an unmade choice should look unmade.
  //
  // Multi-select is exempt, because there an empty selection is an ANSWER.
  // "Which of these should I enable?" can truthfully be answered "none", and
  // gating Send on at least one tick would leave that user with only two ways
  // out: agree to something they don't want, or dismiss the card — which
  // declines every OTHER question in the set along with it.
  const ready = questions.every((_, i) => isMulti(i) || answerFor(i) !== '')

  const send = (
    answers: { answer: string; answers?: string[]; note?: string; declined?: boolean }[],
  ) => onAnswer(request.ask_id, answers)

  const submit = (event: Event) => {
    event.preventDefault()
    if (!ready) return
    send(
      questions.map((_, i) => {
        const n = noteFor(i)
        // The list rides alongside the joined mirror only where it means
        // something. Sending `answers` for a single-choice question would
        // tell the daemon a set was chosen when one thing was.
        const out: { answer: string; answers?: string[]; note?: string } = { answer: answerFor(i) }
        if (isMulti(i)) out.answers = chosenFor(i)
        if (n) out.note = n
        return out
      }),
    )
  }

  // A skip is every question declined, not a short reply: the daemon
  // indexes answers positionally, so a set has to come back the length it
  // went out.
  const skip = () => {
    if (!skipArmed) {
      setSkipArmed(true)
      return
    }
    send(questions.map(() => ({ answer: '', declined: true })))
  }

  const choose = (i: number, option: string) => {
    goto(i)
    if (isMulti(i)) {
      const marks = ticked[i] ?? []
      setTicked({
        ...ticked,
        [i]: marks.includes(option) ? marks.filter((o) => o !== option) : [...marks, option],
      })
      return
    }
    // A lone question still answers on click — unless a note is open, in
    // which case clicking an option is the user CHOOSING what to annotate,
    // not finishing.
    if (single && !noting[i]) {
      send([{ answer: option }])
      return
    }
    setTyping({ ...typing, [i]: false })
    setPicked({ ...picked, [i]: option })
  }

  // What the stub shows to the right of the question. An empty
  // multi-select that has been visited is an answer and has to read as
  // one — left blank it looks like a rendering fault, and it is the row a
  // user would most want to catch before sending.
  const summaryFor = (i: number): string => {
    const a = answerFor(i)
    if (a) return a
    if (isMulti(i) && visited[i]) return t('(none of the options)')
    return ''
  }

  return (
    <form class="card ask" onSubmit={submit}>
      {!single && (
        // Numbers, plus the model's own short name for a question where it
        // gave one. A slug clipped to fit tells you less than the position
        // does, so an unnamed question stays a bare number rather than
        // borrowing the first words of its text.
        <div class="ask-strip">
          {questions.map((q, i) => (
            <button
              type="button"
              key={i}
              class={'ask-chip' + (tab === i ? ' on' : '')}
              aria-current={tab === i ? 'true' : undefined}
              onClick={() => goto(i)}
            >
              {i + 1}
              {q.slug ? ' ' + q.slug : ''}
              {settled(i) ? ' ✓' : ''}
            </button>
          ))}
          <span class="ask-strip__sep" aria-hidden="true" />
          <button
            type="button"
            class={'ask-chip' + (tab === 'review' ? ' on' : '')}
            aria-current={tab === 'review' ? 'true' : undefined}
            onClick={() => goto('review')}
          >
            {t('review')}
          </button>
        </div>
      )}
      <div class="ask-body">
        {questions.map((q, i) => {
          const free = isFree(i)
          const multi = isMulti(i)
          if (folded(i)) {
            const summary = summaryFor(i)
            return (
              <div class="ask-question" key={i}>
                {/* A button, not a div with a handler: this is the way to
                    the question, so it has to be reachable by keyboard and
                    announced as expandable. */}
                <button
                  type="button"
                  class={'ask-summary' + (summary ? '' : ' ask-summary--todo')}
                  aria-expanded={false}
                  onClick={() => goto(i)}
                >
                  <span class="ask-num">{i + 1}.</span>
                  <span class="ask-summary__q">{q.question}</span>
                  {!!summary && <span class="ask-summary__a">{summary}</span>}
                  {!!noteFor(i) && <span class="ask-summary__note">{t('note')}</span>}
                </button>
              </div>
            )
          }
          return (
            <div class="ask-question" key={i}>
              <div class="card-head">
                {questions.length > 1 && <span class="ask-num">{i + 1}.</span>} {q.question}
              </div>
              {!!q.options?.length && (
                <div class="card-actions">
                  {q.options.map((option) => {
                    const on = multi
                      ? (ticked[i] ?? []).includes(option)
                      : !single && !typing[i] && picked[i] === option
                    return (
                      <button
                        type="button"
                        key={option}
                        // aria-pressed is what tells a screen reader this is a
                        // toggle rather than a command — without it a ticked
                        // box and an unticked one are announced identically,
                        // and the checkmark below is decoration only.
                        aria-pressed={multi ? on : undefined}
                        class={'btn' + (on ? ' primary' : '')}
                        onClick={() => choose(i, option)}
                      >
                        {multi ? (on ? '☑ ' : '☐ ') : ''}
                        {option}
                      </button>
                    )
                  })}
                  {q.allow_custom && (
                    <button
                      type="button"
                      aria-pressed={multi ? !!typing[i] : undefined}
                      class={'btn' + (typing[i] ? ' primary' : '')}
                      onClick={() => {
                        goto(i)
                        setTyping({ ...typing, [i]: !multi || !typing[i] })
                      }}
                    >
                      {multi
                        ? (typing[i] ? '☑ ' : '☐ ') + t('Add my own…')
                        : t('Type my own answer…')}
                    </button>
                  )}
                </div>
              )}
              {showsCustomInput(i) && (
                <div class="ask-custom">
                  <input
                    value={custom[i] ?? ''}
                    onInput={(event) =>
                      setCustom({ ...custom, [i]: (event.target as HTMLInputElement).value })
                    }
                    placeholder={t('custom answer…')}
                  />
                </div>
              )}
              {/* The note affordance sits under the options and only where it
                  means something: free text is already the user's own words, so
                  there is nothing for a note to add to it, and an unanswered
                  question has no choice to annotate yet. Gating on an answer
                  also keeps it off the initial render, where it was costing a
                  full button row per question for the rarest action on the
                  card.

                  A LONE question is exempt, and must be: it answers on click,
                  so waiting for an answer would mean the note button appears
                  only after the card has already sent itself. There the note
                  has to come first, which is what suppresses the send. */}
              {!free && !!q.options?.length && !noting[i] && (single || answerFor(i) !== '') && (
                <div class="card-actions">
                  <button
                    type="button"
                    class="btn"
                    onClick={() => {
                      goto(i)
                      setNoting({ ...noting, [i]: true })
                    }}
                  >
                    {t('Add a note…')}
                  </button>
                </div>
              )}
              {!free && !!noting[i] && (
                <div class="ask-custom">
                  <input
                    value={note[i] ?? ''}
                    onInput={(event) =>
                      setNote({ ...note, [i]: (event.target as HTMLInputElement).value })
                    }
                    placeholder={t('note on your answer — sent with it, not instead of it')}
                  />
                </div>
              )}
            </div>
          )
        })}
      </div>
      {skipArmed && (
        <div class="ask-warn" role="alert">
          {single
            ? t('Press skip again — the agent proceeds without your answer')
            : t('Press skip again — the agent proceeds without any of your answers')}
        </div>
      )}
      {showSubmit && (
        <div class="card-actions ask-send">
          <button class="btn primary" type="submit" disabled={!ready}>
            {single ? t('Send') : t('Send answers')}
          </button>
          {/* Dismissing the card already declines the whole set, silently.
              Saying so with a button makes the quiet exit an explicit one. */}
          <button type="button" class={'btn' + (skipArmed ? ' danger' : '')} onClick={skip}>
            {single ? t('Skip') : t('Skip all')}
          </button>
        </div>
      )}
    </form>
  )
}
