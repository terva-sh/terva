import { t } from '../i18n'
import type { ReasoningRungInfo } from '../platform/ctrlproto/types'

// The ladder, lowest to highest, mirroring provider.ReasoningLevels in Go.
// 'inherit' is the null row: it clears the session's override rather than
// setting a level, so it carries '' as its value — the same distinction the
// wire draws, where '' means "follow the global" and 'off' means "chosen off".
//
// This list is only the ORDER and the labels. What each rung DOES arrives from
// the daemon per model (see rungDetail), because it is not a property of the
// ladder: the same 'medium' is 8192 thinking tokens on Claude, effort 'medium'
// on Codex, and effort 'HIGH' — indistinguishable from three other rungs — on
// Gemini 3.
export const REASONING_LEVELS = [
  'off',
  'minimum',
  'low',
  'medium',
  'high',
  'maximum',
  'max',
] as const

export interface ReasoningPickProps {
  // override is the session's own level, '' when it follows the global.
  override: string
  // global is the workspace default, so the inherit row can say what
  // inheriting would actually mean rather than just "inherit".
  global: string
  // modelDefault is the current model's own default, which is what applies
  // when there is no global either. '' when the model states none.
  modelDefault?: string
  // maxIsNative says whether 'max' reaches this model as a native max effort
  // or gets clamped to maximum. Mirrors provider.MaxIsNative.
  maxIsNative?: boolean
  // rungs is what each rung sends on the CURRENT model, from
  // models.list's reasoning_ladders. Undefined means the model takes no
  // thinking setting at all — which is not the same as every rung being off.
  rungs?: ReasoningRungInfo[]
  onPick: (level: string) => void
  onClose: () => void
}

// rungDetail is the one-line explanation for a rung: what this model actually
// receives when you pick it.
//
// It reports the WIRE VALUE rather than a ladder-wide token budget. The budget
// was the wrong one nearly everywhere — the Codex/Responses route and Gemini 3
// take an effort enum and no budget at all, so "~8k tokens of thinking"
// described a number the request never carried. This mirrors reasoningDetail
// in packages/agent/modes/dialogs/reasoning_dialog.go; both read the same
// per-model rows, so neither can invent a number.
export function rungDetail(
  level: string,
  rung: ReasoningRungInfo | undefined,
  maxIsNative?: boolean,
): string {
  if (!rung) return t('this model takes no thinking setting')

  const off = !rung.budget && !rung.effort
  if (off) return t('no thinking')

  const base = rung.budget
    ? t('~%sk tokens of thinking', String(Math.round(rung.budget / 1024)))
    : rung.effort
      ? t('effort: %s', rung.effort)
      : t('thinking on')

  // The top rung keeps its own note: 'max' is the one place a user is choosing
  // a tier that may not exist here, and naming the effort alone would not say
  // so. Native and collapsed are mutually exclusive — a rung that IS the
  // native ceiling is not a duplicate of a lower one.
  if (level === 'max' && !rung.same_as && maxIsNative) {
    return base + t(' — native on this model')
  }
  if (rung.same_as) return base + t(' — same as %s on this model', rung.same_as)
  return base
}

export function ReasoningPick({
  override,
  global,
  modelDefault,
  maxIsNative,
  rungs,
  onPick,
  onClose,
}: ReasoningPickProps) {
  const inheritDetail = global
    ? t('follow the global setting (now: %s)', global)
    : modelDefault
      ? t("follow the model's default (%s)", modelDefault)
      : t('follow the global setting')

  const byLevel = new Map((rungs ?? []).map((r) => [r.level, r]))

  const rows: { level: string; label: string; detail: string }[] = [
    { level: '', label: t('inherit'), detail: inheritDetail },
    ...REASONING_LEVELS.map((lv) => ({
      level: lv as string,
      label: lv as string,
      detail: rungDetail(lv, byLevel.get(lv), maxIsNative),
    })),
  ]

  return (
    <div class="reasoning-pick" role="dialog" aria-label={t('Reasoning for this session')}>
      <div class="reasoning-pick-head">
        <span>{t('Reasoning for this session')}</span>
        <button class="reasoning-pick-close" onClick={onClose} title={t('Close')}>
          ×
        </button>
      </div>
      <ul class="reasoning-pick-list">
        {rows.map((r) => (
          <li key={r.level || 'inherit'}>
            <button
              class={'reasoning-row' + (r.level === override ? ' is-active' : '')}
              onClick={() => {
                onPick(r.level)
                onClose()
              }}
            >
              <span class="reasoning-row-label">{r.label}</span>
              <span class="reasoning-row-detail">{r.detail}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

// reasoningLabel is the compact text for the button that opens the picker: the
// session's own level when it has one, else the global, else nothing worth
// showing. The trailing dot marks an override so the button distinguishes "this
// session is deliberately here" from "this is just the default".
export function reasoningLabel(override: string, global: string): string {
  if (override) return override + ' •'
  return global
}
