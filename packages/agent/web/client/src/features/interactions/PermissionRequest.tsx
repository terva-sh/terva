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
        <button
          class="btn"
          title={t('For the rest of this session')}
          onClick={() => onDecide(request.call_id, { allow: true, remember_tool: true })}
        >
          {t('Allow & remember')}
        </button>
        {request.scopes && request.scopes.length > 0 && (
          <button
            class="btn"
            title={t('Saves a permanent allow rule scoped to this command')}
            onClick={() =>
              onDecide(request.call_id, {
                allow: true,
                persist_tool: true,
                persist_scopes: request.scopes!.map((s) => s.pattern),
              })
            }
          >
            {t('Always allow “%s”', request.scopes.map((s) => s.display).join(', '))}
          </button>
        )}
        <button class="btn danger" onClick={() => onDecide(request.call_id, { allow: false, reason: 'denied by user' })}>
          {t('Deny')}
        </button>
      </div>
    </div>
  )
}
