import { t } from '../i18n'
import type { ModelInfo, ReasoningRungInfo } from '../platform/ctrlproto/types'

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
  // inherit is what a session that overrides nothing actually runs at on this
  // model, and inheritFrom is which layer decided it — both resolved by the
  // daemon (ModelInfo.inherit_reasoning / .inherit_reasoning_from).
  //
  // This used to be (global, modelDefault) and the inherit row worked the chain
  // out here: global first, model default second. That is the wrong way round
  // for an OPERATOR's per-model models.json level, which outranks the global —
  // and the raw model field cannot be told from a CATALOG default without a
  // signal this component was never given. So an operator who set a per-model
  // level was told the session would "follow the global setting", naming a
  // value that was not deciding anything while the turn ran at theirs.
  inherit?: string
  inheritFrom?: ModelInfo['inherit_reasoning_from']
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

// inheritDetailFor is the inherit row's sentence: a SWITCH on which layer the
// daemon says won, never a re-derivation of the chain.
//
// It mirrors the same switch in packages/agent/modes/dialogs/reasoning_dialog.go
// — both read a source the daemon resolved, so neither can disagree with the
// turn about which rung is deciding. Each arm names its own layer: saying
// "global" for a catalog default points the user at a setting that is not the
// one in play, which is the whole failure this replaces.
export function inheritDetailFor(
  level: string,
  from: ModelInfo['inherit_reasoning_from'],
): string {
  switch (from) {
    case 'model_operator':
      return t("follow this model's configured level (%s)", level)
    case 'global':
      return t('follow the global setting (now: %s)', level)
    case 'model_catalog':
      return t("follow the model's default (%s)", level)
    default:
      // Nothing set anywhere, or a source this build does not know. Naming no
      // level beats naming the wrong one.
      return t('follow the global setting')
  }
}

export function ReasoningPick({
  override,
  inherit,
  inheritFrom,
  maxIsNative,
  rungs,
  onPick,
  onClose,
}: ReasoningPickProps) {
  const inheritDetail = inheritDetailFor(inherit ?? '', inheritFrom)

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
// session's own level when it has one, else what it INHERITS, else nothing
// worth showing. The trailing dot marks an override so the button distinguishes
// "this session is deliberately here" from "this is just the default".
//
// The second argument used to be the global, which is only one of the three
// layers a session can inherit from — so a session on a model carrying an
// operator's per-model level showed the global's value, or, where no global was
// passed at all, showed the ◐ placeholder as though nothing were set. Pass
// ModelInfo.inherit_reasoning: the daemon has already picked the winner.
export function reasoningLabel(override: string, inherit: string): string {
  if (override) return override + ' •'
  return inherit
}
