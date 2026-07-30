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
  onAnswer: (id: string, answers: { answer: string }[]) => void
}) {
  const questions = askQuestions(request)
  // A lone question keeps its original one-click answer: there is nothing
  // else on the card to coordinate with, so making the user press Send
  // after picking would be pure friction. A set has to be submitted —
  // same split the TUI makes between one question and a tabbed set.
  const single = questions.length === 1
  const [picked, setPicked] = useState<Record<number, string>>({})
  const [custom, setCustom] = useState<Record<number, string>>({})
  const [typing, setTyping] = useState<Record<number, boolean>>({})

  // A question shows a text input when it has no options at all, or when
  // the user reached for "type my own answer".
  const isFree = (i: number) => !questions[i].options?.length || !!typing[i]

  const answerFor = (i: number): string => {
    if (isFree(i)) return (custom[i] ?? '').trim()
    return picked[i] ?? ''
  }

  // A lone question that is showing only its option buttons needs no
  // submit — clicking an option sends. Rendering one anyway left a dead,
  // permanently-disabled Send under the options with nothing to submit.
  const showSubmit = !single || isFree(0)

  // Every question needs something to send. A choice defaults to nothing
  // picked rather than to the first option: on a form the user can see
  // all at once, an unmade choice should look unmade.
  const ready = questions.every((_, i) => answerFor(i) !== '')

  const send = (answers: string[]) => onAnswer(request.ask_id, answers.map((answer) => ({ answer })))

  const submit = (event: Event) => {
    event.preventDefault()
    if (!ready) return
    send(questions.map((_, i) => answerFor(i)))
  }

  const choose = (i: number, option: string) => {
    if (single) {
      send([option])
      return
    }
    setTyping({ ...typing, [i]: false })
    setPicked({ ...picked, [i]: option })
  }

  return (
    <form class="card ask" onSubmit={submit}>
      {questions.map((q, i) => {
        const free = isFree(i)
        return (
          <div class="ask-question" key={i}>
            <div class="card-head">
              {questions.length > 1 && <span class="ask-num">{i + 1}.</span>} {q.question}
            </div>
            {!!q.options?.length && (
              <div class="card-actions">
                {q.options.map((option) => (
                  <button
                    type="button"
                    key={option}
                    class={'btn' + (!single && !typing[i] && picked[i] === option ? ' primary' : '')}
                    onClick={() => choose(i, option)}
                  >
                    {option}
                  </button>
                ))}
                {q.allow_custom && (
                  <button
                    type="button"
                    class={'btn' + (typing[i] ? ' primary' : '')}
                    onClick={() => setTyping({ ...typing, [i]: true })}
                  >
                    {t('Type my own answer…')}
                  </button>
                )}
              </div>
            )}
            {free && (
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
