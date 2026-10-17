import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootTemplate } from '../../platform/ctrlproto/types'

const NAME = /^[a-z][a-z0-9-]*$/

// SaveTemplate saves a running team's roster as one of the person's own
// templates. The daemon drops the team's home, colour, and budget waiver, so
// the template starts the next team in any directory.
export function SaveTemplate({ client, id, onError }: { client: ClientLike; id: string; onError: (error: string) => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState('')

  const problem = name && !NAME.test(name) ? t('Use lower case letters, digits, and dashes, starting with a letter.') : ''
  const save = () => {
    if (saving || !name || problem) return
    setSaving(true)
    void client
      .send<TalkootTemplate>('talkoot.save_template', { id, name, description: description.trim() || undefined })
      .then(
        (tm) => {
          onError('')
          setSaved(tm.name)
          setOpen(false)
          setName('')
          setDescription('')
        },
        (e: unknown) => onError(e instanceof Error ? e.message : String(e)),
      )
      .finally(() => setSaving(false))
  }

  if (!open) {
    return (
      <div class="talkoot-save-template">
        <button class="btn sm" onClick={() => { setOpen(true); setSaved(''); setName(id) }}>
          {t('Save as template')}
        </button>
        {saved && <span class="talkoot-note">{t('Saved as %s. The New team form lists it.', saved)}</span>}
      </div>
    )
  }
  return (
    <div class="talkoot-save-template">
      <label class="talkoot-field">
        <span>{t('Template name')}</span>
        <input value={name} aria-label={t('Template name')} onInput={(e) => setName((e.target as HTMLInputElement).value.trim())} />
      </label>
      {problem && <p class="talkoot-error">{problem}</p>}
      <label class="talkoot-field">
        <span>{t('Description')}</span>
        <input value={description} placeholder={t('optional')} onInput={(e) => setDescription((e.target as HTMLInputElement).value)} />
      </label>
      <p class="talkoot-note">{t('The template keeps the members, budget, and charter, and leaves out this team\'s directory and colour.')}</p>
      <div class="card-actions">
        <button class="btn sm primary" disabled={saving || !name || !!problem} onClick={save}>
          {t('Save')}
        </button>
        <button class="btn sm ghost" onClick={() => setOpen(false)}>
          {t('Cancel')}
        </button>
      </div>
    </div>
  )
}
