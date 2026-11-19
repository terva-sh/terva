import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import { sourceLabel } from '../../platform/conversation/source'

// QueuedMessage is a pending user message shown while a turn runs. It can be
// edited in place or removed before the agent consumes it.
//
// source is set for a text that terva queued, such as the report on a batch of
// sub-agents. That chip names its writer and offers no edit, because the text
// is not the person's draft. It can still be removed.
export function QueuedMessage({
  text,
  source,
  onEdit,
  onCancel,
}: {
  text: string
  source?: string
  onEdit: (text: string) => void
  onCancel: () => void
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(text)
  const editRef = useRef<HTMLTextAreaElement>(null)
  useEffect(() => {
    if (editing) editRef.current?.focus()
  }, [editing])
  // The timeline keys a chip by its position, so this component outlives the
  // entry it was drawn for: when the entry ahead leaves the queue, the next
  // one moves in. An open editor belongs to the entry that left. Close it, or
  // the person edits and sends a text that they never opened.
  useEffect(() => {
    setEditing(false)
    setDraft(text)
  }, [text, source])
  // 🚨 A text that terva queued never reaches the editor, whatever state this
  // component was left in. An edit sends the text back as the person's own.
  if (editing && !source) {
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
  if (source) {
    return (
      <div class="msg queued sourced" title={t('queued by terva — sends when the current turn reaches a safe point')}>
        <span class="queued-mark">⟳</span>
        <span class="queued-text">
          <span class="sys-tag">{sourceLabel(source)}</span> {text}
        </span>
        <span class="queued-tools">
          <button class="icon sm" title={t('Remove from queue')} onClick={onCancel}>
            ×
          </button>
        </span>
      </div>
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
