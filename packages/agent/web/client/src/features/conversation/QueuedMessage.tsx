import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'

// QueuedMessage is a pending user message shown while a turn runs. It can be
// edited in place or removed before the agent consumes it.
export function QueuedMessage({
  text,
  onEdit,
  onCancel,
}: {
  text: string
  onEdit: (text: string) => void
  onCancel: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(text)
  const editRef = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    if (editing) editRef.current?.focus()
  }, [editing])
  if (editing) {
    return (
      <form
        class="msg user queued editing"
        onSubmit={(event) => {
          event.preventDefault()
          setEditing(false)
          if (draft.trim() !== text) onEdit(draft)
        }}
      >
        <textarea
          value={draft}
          rows={1}
          ref={editRef}
          onInput={(event) => setDraft((event.target as HTMLTextAreaElement).value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault()
              ;(event.currentTarget.form as HTMLFormElement).requestSubmit()
            } else if (event.key === 'Escape') {
              setDraft(text)
              setEditing(false)
            }
          }}
        />
        <div class="queued-actions">
          <button type="submit" class="btn sm primary">
            {t('Save')}
          </button>
          <button
            type="button"
            class="btn sm"
            onClick={() => {
              setDraft(text)
              setEditing(false)
            }}
          >
            {t('Cancel')}
          </button>
        </div>
      </form>
    )
  }
  return (
    <div class="msg user queued" title={t('queued — sends when the current turn reaches a safe point')}>
      <span class="queued-mark">⟳</span>
      <span class="queued-text">{text}</span>
      <span class="queued-tools">
        <button
          class="icon sm"
          title={t('Edit queued message')}
          onClick={() => {
            setDraft(text)
            setEditing(true)
          }}
        >
          ✎
        </button>
        <button class="icon sm" title={t('Remove from queue')} onClick={onCancel}>
          ×
        </button>
      </span>
    </div>
  )
}
