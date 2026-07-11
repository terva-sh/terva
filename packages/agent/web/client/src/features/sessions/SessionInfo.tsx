import { t } from '../../i18n'
import type { SessionInfo as SessionInfoData } from '../../platform/ctrlproto/types'
import { copyToClipboard } from '../../ui/browser'

export function SessionInfo({
  info,
  cost,
  onClose,
  onContext,
}: {
  info: SessionInfoData | null
  cost: number
  onClose: () => void
  onContext: () => void
}) {
  if (!info) return null
  const copy = (text: string) => void copyToClipboard(text)
  return (
    <div class="info-scrim" onClick={onClose}>
      <div class="info-pop" onClick={(event) => event.stopPropagation()}>
        <div class="info-row">
          <span>{t('Persona')}</span>
          <b>{info.persona || '—'}</b>
        </div>
        <div class="info-row">
          <span>{t('Model')}</span>
          <b>
            {info.provider ? info.provider + ' / ' : ''}
            {info.model || '—'}
          </b>
        </div>
        <div class="info-row">
          <span>{t('Messages')}</span>
          <b>{info.messages}</b>
        </div>
        <div class="info-row">
          <span>{t('Cost')}</span>
          <b>${cost.toFixed(4)}</b>
        </div>
        <div class="info-row">
          <span>{t('Session')}</span>
          <b class="mono">{info.id}</b>
        </div>
        {info.path && (
          <div class="info-row path">
            <span>{t('Path')}</span>
            <code class="mono" title={info.path}>
              {info.path}
            </code>
            <button class="btn sm" onClick={() => copy(info.path!)}>
              {t('copy')}
            </button>
          </div>
        )}
        <div class="info-actions">
          <button class="btn sm" onClick={onContext}>
            {t('Context breakdown →')}
          </button>
        </div>
      </div>
    </div>
  )
}
