import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import { askQuestions, type AskRequest as AskRequestData } from '../../platform/ctrlproto/types'

// One mid-turn ask. A set of questions is stacked in a single card with
// one submit, so the whole set costs the user one interruption — the TUI
// shows the same set as tabs. Answers go back positionally, one per
// question, which is what the daemon indexes them by.
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
    answers: { answer: string; answers?: string[]; note?: string }[],
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

  const send = (answers: { answer: string; answers?: string[]; note?: string }[]) =>
    onAnswer(request.ask_id, answers)

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

  const choose = (i: number, option: string) => {
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

  return (
    <form class="card ask" onSubmit={submit}>
      {questions.map((q, i) => {
        const free = isFree(i)
        const multi = isMulti(i)
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
                    onClick={() => setTyping({ ...typing, [i]: !multi || !typing[i] })}
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
            {/* The note affordance sits under the options and only where
                it means something: free text is already the user's own
                words, so there is nothing for a note to add to it. */}
            {!free && !!q.options?.length && !noting[i] && (
              <div class="card-actions">
                <button
                  type="button"
                  class="btn"
                  onClick={() => setNoting({ ...noting, [i]: true })}
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
      {showSubmit && (
        <div class="card-actions">
          <button class="btn primary" type="submit" disabled={!ready}>
            {single ? t('Send') : t('Send answers')}
          </button>
        </div>
      )}
    </form>
  )
}
