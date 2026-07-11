import { t } from '../../i18n'
import type { Decision, PermissionRequest as PermissionRequestData } from '../../platform/ctrlproto/types'
import { truncate } from '../../ui/formatting'

export function PermissionRequest({
  request,
  onDecide,
}: {
  request: PermissionRequestData
  onDecide: (id: string, decision: Decision) => void
}) {
  return (
    <div class="card perm">
      <div class="card-head">
        {t('Approve tool:')} <code>{request.tool}</code>
      </div>
      {request.preview && <pre class="preview">{truncate(request.preview, 1500)}</pre>}
      <div class="card-actions">
        <button class="btn primary" onClick={() => onDecide(request.call_id, { allow: true })}>
          {t('Allow')}
        </button>
        <button class="btn" onClick={() => onDecide(request.call_id, { allow: true, remember_tool: true })}>
          {t('Allow & remember')}
        </button>
        <button class="btn danger" onClick={() => onDecide(request.call_id, { allow: false, reason: 'denied by user' })}>
          {t('Deny')}
        </button>
      </div>
    </div>
  )
}
