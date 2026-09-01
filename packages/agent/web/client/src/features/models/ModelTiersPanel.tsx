import type {
  ModelInfo,
  ModelTierRung,
  ModelTiersView,
  ReasoningRungInfo,
} from '../../platform/ctrlproto/types'
import { t } from '../../i18n'

// A provider's sub-agent tier ladder: the model `swarm_spawn` gets for
// `tier: weak`, `medium`, `strong` or `cheap`, and the model a RAATI seat is
// filled with at rigor level 1.
//
// It shows what each rung RESOLVES to, never merely what config holds. An empty
// ladder in config is the ordinary case and says nothing about whether the
// ladder is right: google's medium and strong rungs once resolved to
// image-generation models on a stock install, with every automated check
// passing, and a panel that listed overrides would have shown three blank rows
// the whole time.

// tierState reduces a rung to the three answers worth telling apart: you set
// it, a built-in rule set it, or nothing did. It drives the dot, the badge and
// the row's left border from one value, so those three can never disagree.
export function tierState(r: ModelTierRung): 'override' | 'built-in' | 'empty' {
  if (!r.model) return 'empty'
  return r.source === 'override' ? 'override' : 'built-in'
}

export function ModelTiersPanel({
  view,
  models,
  ladders,
  busy,
  error,
  onSet,
  onReset,
  onClose,
}: {
  view: ModelTiersView
  // This provider's models, for the per-rung picker.
  models: ModelInfo[]
  ladders: Record<string, ReasoningRungInfo[]>
  busy: boolean
  error: string
  onSet: (rung: string, model: string, reasoning: string) => void
  onReset: (rung: string) => void
  onClose: () => void
}) {
  // The levels THIS rung's model can actually tell apart. A ladder collapses:
  // Gemini 3 lands medium/high/maximum/max on one value, so offering all four
  // would be four names for one choice. same_as is the daemon's own annotation
  // for that, and the session thinking picker reads it the same way.
  const levelsFor = (r: ModelTierRung): string[] => {
    const key = models.find((m) => m.id === r.model)?.ladder
    const rungs = key ? ladders[key] : undefined
    return (rungs ?? []).filter((x) => !x.same_as).map((x) => x.level)
  }

  return (
    <div class="prov-flow">
      <div class="prov-flow-title">{t('sub-agent tiers · %s', view.provider)}</div>
      <div class="prov-note">
        {t('The model a sub-agent is spawned with for each tier. Empty follows terva’s built-in guess.')}
      </div>

      <div class="tier-list">
        {(view.rungs ?? []).map((r) => {
          const state = tierState(r)
          return (
            <div key={r.rung} class="tier-row" data-source={state}>
              <div class="tier-top">
                <span class="tier-dot" aria-hidden="true" />
                <span class="tier-name">{r.rung}</span>
                {/* "Nobody pinned this" and "this is wrong" are different
                    answers, and only the source tells them apart. */}
                <span class="tier-src">
                  {state === 'override' ? t('set') : state === 'built-in' ? t('built-in') : t('empty')}
                </span>
              </div>

              <div class="tier-resolved">
                {r.model
                  ? r.label || r.model
                  : // Not "off": a rung that resolves to nothing means a
                    // sub-agent asking for this tier quietly runs on the HOST
                    // model, at the host's cost and speed.
                    t('falls back to the host model')}
              </div>

              <div class="tier-controls">
                {/* The picker carries the PIN, while the line above carries the
                    resolved model. Showing the resolved id here would make an
                    untouched rung look pinned, and saving it would freeze a rung
                    that had been tracking its family rule. */}
                <select
                  value={r.pinned ?? ''}
                  disabled={busy}
                  aria-label={t('%s: model', r.rung)}
                  onChange={(e) =>
                    onSet(r.rung, (e.target as HTMLSelectElement).value, r.reasoning ?? '')
                  }
                >
                  <option value="">
                    {r.model && r.source === 'built-in'
                      ? t('built-in (%s)', r.model)
                      : t('built-in')}
                  </option>
                  {models.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.id}
                    </option>
                  ))}
                </select>

                <select
                  value={r.reasoning ?? ''}
                  disabled={busy || !r.model}
                  aria-label={t('%s: thinking', r.rung)}
                  onChange={(e) =>
                    onSet(r.rung, r.pinned ?? '', (e.target as HTMLSelectElement).value)
                  }
                >
                  <option value="">{t('thinking: leave to the sub-agent')}</option>
                  {levelsFor(r).map((lv) => (
                    <option key={lv} value={lv}>
                      {t('thinking: %s', lv)}
                    </option>
                  ))}
                </select>

                {r.source === 'override' && (
                  <button class="tier-reset" disabled={busy} onClick={() => onReset(r.rung)}>
                    {t('Reset rung')}
                  </button>
                )}
              </div>
            </div>
          )
        })}
      </div>

      {error ? <div class="prov-warn">{error}</div> : null}

      <div class="prov-actions">
        <button class="btn" disabled={busy} onClick={onClose}>
          {t('Done')}
        </button>
      </div>
    </div>
  )
}
