import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootView } from '../../platform/ctrlproto/types'

// The waiver survives a reload because the roster, not the browser, owns it.
// Its toggle changes no other team or member field.
export function TeamBudget({ client, view, person, canSteer, onError }: {
  client: ClientLike
  view: TalkootView
  person: string
  canSteer: boolean
  onError: (error: string) => void
}) {
  const [saving, setSaving] = useState(false)
  const change = () => {
    if (saving) return
    setSaving(true)
    void client.send('talkoot.update', { id: view.id, by: person, team_budget_waived: !view.team_budget_waived }).then(
      () => onError(''),
      (e: unknown) => onError(e instanceof Error ? e.message : String(e)),
    ).finally(() => setSaving(false))
  }
  return (
    <div class="talkoot-budget">
      <div class="talkoot-note">
        {view.team_budget_waived
          ? t('Team daily limit waived')
          : t('Team daily limit: $%s', (view.budget_usd_per_day ?? 0).toFixed(2))}
      </div>
      {view.team_budget_waived && <div class="talkoot-note">{t('Member limits still apply. The waiver stays on until you restore the team limit.')}</div>}
      {canSteer && (
        <button class="btn sm" disabled={saving} onClick={change}>
          {view.team_budget_waived ? t('Restore team daily limit') : t('Waive team daily limit')}
        </button>
      )}
    </div>
  )
}
