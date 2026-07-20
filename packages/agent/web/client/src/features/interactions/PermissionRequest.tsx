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
        {/* No "always — save to config" button yet, though the wire and the gate
            both carry persist_tool: nothing installs ConfirmGate.SetPersist in a
            production build, so the durable grant silently degrades to this
            session only. Offering it here would repeat what the TUI's dialog
            already claims and does not do. See docs/reviews/2026-07-20. */}
        <button class="btn danger" onClick={() => onDecide(request.call_id, { allow: false, reason: 'denied by user' })}>
          {t('Deny')}
        </button>
      </div>
    </div>
  )
}
