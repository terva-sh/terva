import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { AskRequest as AskRequestData } from '../../platform/ctrlproto/types'

export function AskRequest({
  request,
  onAnswer,
}: {
  request: AskRequestData
  onAnswer: (id: string, text: string) => void
}) {
  const [custom, setCustom] = useState('')
  return (
    <div class="card ask">
      <div class="card-head">{request.question}</div>
      <div class="card-actions">
        {(request.options ?? []).map((option) => (
          <button class="btn" onClick={() => onAnswer(request.ask_id, option)}>
            {option}
          </button>
        ))}
      </div>
      {request.allow_custom && (
        <form
          class="ask-custom"
          onSubmit={(event) => {
            event.preventDefault()
            if (custom.trim()) onAnswer(request.ask_id, custom.trim())
          }}
        >
          <input
            value={custom}
            onInput={(event) => setCustom((event.target as HTMLInputElement).value)}
            placeholder={t('custom answer…')}
          />
          <button class="btn primary" type="submit">
            {t('Send')}
          </button>
        </form>
      )}
    </div>
  )
}
