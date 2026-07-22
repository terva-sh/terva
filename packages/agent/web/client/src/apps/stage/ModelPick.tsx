import { useEffect, useState } from 'preact/hooks'
import { m, t, tr } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { ModelInfo } from '../../platform/ctrlproto/types'

// Compact context-window label (200000 → "200k"). Stage keeps its own tiny
// helper rather than pull the panel's humanCount (and its i18n) across.
function ctxLabel(n?: number): string {
  if (!n) return ''
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n)
}

// How a provider bills, rendered as a tiny chip. 'oauth' means a subscription
// plan is footing the bill, 'apikey' means metered per-token spend — the
// distinction you want BEFORE picking, when the same model id is reachable both
// ways. Undefined (keyless backends: ollama, named endpoints) renders nothing:
// we do not know, and a confident wrong badge is worse than no badge.
// The titles are m()-marked and tr()-translated where rendered; the badge
// texts are i18n-exempt — 'sub'/'api' are compact technical tokens, not prose.
const AUTH_LABEL: Record<string, { text: string; title: string }> = {
  oauth: { text: 'sub', title: m('Subscription — this provider is signed in with a plan, not a metered key') },
  apikey: { text: 'api', title: m('API key — usage on this provider is billed per token') },
}

function AuthChip({ auth }: { auth?: string }) {
  const l = auth ? AUTH_LABEL[auth] : undefined
  if (!l) return null
  return (
    <span class={`stage-modelpick__auth stage-modelpick__auth--${auth}`} title={tr(l.title)}>
      {l.text}
    </span>
  )
}

// ModelPick is the Stage-native model/provider picker, shared by every model
// surface in Stage: the chat header's one-tap switch (compact), the steering
// drawer's live session switch, the per-actor cast pin, the Suggest/Direct
// per-generation override, and the card-doctor override.
// It lists the daemon's logged-in models grouped by provider with a "★ Favorites"
// section floated to the top of the list — in BOTH modes, so a favourite is one
// tap away wherever a model is chosen. It reuses the panel's verbs
// (models.list/switch/favorite) but renders on Stage's own skin instead of
// importing the panel's picker + CSS. With onSelect it SELECTS for one action
// (Phase-7 routing); without, it switches the session LIVE via models.switch —
// effective on the next message, no recreate. The current model tracks info.model
// from the snapshot (updated by the session_updated the daemon broadcasts).
export function ModelPick(props: {
  client: ClientLike
  sessionId: string
  currentProvider?: string
  currentModel?: string
  // When onSelect is set, the picker SELECTS a model for a single action (Phase 7
  // per-generation routing) instead of switching the session live: a row reports
  // {provider, model} and a "Default" row clears the override (empty strings).
  // currentProvider/currentModel then mark the currently-selected override, and
  // sessionId is unused (the daemon never switches).
  onSelect?: (provider: string, model: string) => void
  defaultLabel?: string
  // What defaultLabel actually RESOLVES to. A label like "Session model" names
  // the routing, not the model: it says this action inherits, but not what it
  // inherits — and after a switch or two, or with the same model id reachable
  // through two providers, that is the whole question. Callers that know the
  // inherited model pass it here; see the catalog fallback below for those that
  // don't.
  defaultProvider?: string
  defaultModel?: string
  // Compact renders the collapsed control as a bare header chip (just "★ id",
  // like the panel's model button) instead of the drawer's labeled block, and
  // floats the open list as an absolute popover so it overlays rather than pushes
  // the header taller. Header use only; live-switch mode (no onSelect).
  compact?: boolean
}) {
  const { client, sessionId, currentProvider, currentModel, onSelect, defaultLabel, compact } = props
  const { defaultProvider, defaultModel } = props
  const selecting = typeof onSelect === 'function'
  const [models, setModels] = useState<ModelInfo[]>([])
  const [open, setOpen] = useState(false)
  const [error, setError] = useState('')

  const load = () =>
    client
      .send<{ models: ModelInfo[] }>('models.list', {}, '')
      .then((r) => setModels(r.models ?? []))
      .catch((e: unknown) => setError(String(e)))

  // Load the catalog whenever the picker opens — a chat that never touches the
  // model list never pays for it, and reopening refetches so a provider logged in
  // since the last open (a new Kimi key, say) shows up without remounting the
  // component. The prior list stays on screen until the response lands, so there
  // is no flash to empty.
  useEffect(() => {
    if (open) void load()
  }, [open])

  // Group by provider in catalog order; favorites get their own section at the
  // top of the list (below) in every mode, mirroring the panel + TUI ordering
  // rather than floating within each group (which duplicated them under ★).
  const groups = (() => {
    const g = new Map<string, ModelInfo[]>()
    for (const m of models) {
      const arr = g.get(m.provider) ?? []
      arr.push(m)
      g.set(m.provider, arr)
    }
    return [...g.entries()]
  })()
  const favorites = models.filter((m) => m.favorite)

  const isCurrent = (m: ModelInfo) => m.id === currentModel && m.provider === currentProvider

  // The model behind defaultLabel, resolved as far as we can. An explicit
  // defaultModel is matched against the catalog so the row can also carry the
  // provider's billing chip, but falls back to the bare props — the collapsed
  // button must name the inherited model BEFORE the list has ever been opened,
  // and the catalog loads lazily. With no explicit default (the card doctor's
  // "Workspace model", which has no session to ask) we use the catalog's own
  // default entry; that one is only known once the list is open, which is where
  // it matters and the only place it renders.
  const inherited: ModelInfo | undefined = defaultModel
    ? models.find((m) => m.id === defaultModel && m.provider === defaultProvider) ?? {
        id: defaultModel,
        provider: defaultProvider ?? '',
      }
    : models.find((m) => m.default)
  const inheritedTitle = inherited
    ? `${defaultLabel || t('Default')}: ${inherited.id}${inherited.provider ? ` (${inherited.provider})` : ''}`
    : ''

  // Fire-and-forget: the session_updated snapshot re-renders info.model, so we
  // just close. Session id rides the frame (the switch targets THIS session).
  const switchTo = (m: ModelInfo) => {
    if (selecting) {
      onSelect!(m.provider, m.id)
      setOpen(false)
      return
    }
    if (isCurrent(m)) {
      setOpen(false)
      return
    }
    client.fire('models.switch', { model: m.id, provider: m.provider }, sessionId)
    setOpen(false)
  }
  const toggleFav = (m: ModelInfo, on: boolean) => {
    client.send('models.favorite', { provider: m.provider, model: m.id, on }, '').catch((e: unknown) => setError(String(e)))
    setModels((ms) => ms.map((x) => (x.id === m.id && x.provider === m.provider ? { ...x, favorite: on } : x)))
  }

  // One row, shared by the favorites section and the provider groups. keyPrefix
  // namespaces the two so a favorited model can render in both without a key clash.
  //
  // showProvider carries the row's provenance INTO the row. A provider group
  // already names its provider in the header above, but ★ Favorites is a flat
  // cross-provider list — so two providers offering the same model id rendered as
  // two identical rows there, and picking between them was a coin flip. That is
  // the bug this argument fixes; the group rows stay clean because their heading
  // already says it.
  const renderRow = (m: ModelInfo, keyPrefix: string, showProvider = false) => (
    <div
      key={keyPrefix + m.provider + '/' + m.id}
      class={`stage-modelpick__row ${isCurrent(m) ? 'stage-modelpick__row--current' : ''}`}
      onClick={() => switchTo(m)}
    >
      <button
        class="stage-modelpick__star"
        title={m.favorite ? t('Unfavorite') : t('Favorite')}
        onClick={(e) => {
          e.stopPropagation()
          toggleFav(m, !m.favorite)
        }}
      >
        {m.favorite ? '★' : '☆'}
      </button>
      <span class="stage-modelpick__id">{m.id}</span>
      {showProvider && (
        <span class="stage-modelpick__prov" title={t('Provider: %s', m.provider)}>
          {m.provider}
        </span>
      )}
      {showProvider && <AuthChip auth={m.auth} />}
      {m.context_window ? <span class="stage-modelpick__meta">{ctxLabel(m.context_window)}</span> : null}
    </div>
  )

  // The header chip's label: "★ id" when the current model is a favourite (known
  // once the list has been opened once — before that we just name the id), else a
  // bare "model" placeholder when the session has no model resolved yet.
  const chipModel = models.find(isCurrent)
  const chipLabel = currentModel ? `${chipModel?.favorite ? '★ ' : ''}${currentModel}` : t('model')

  return (
    <div class={`stage-modelpick${compact ? ' stage-modelpick--compact' : ''}`}>
      {/* A tap-anywhere backdrop dismisses the header popover — it floats over the
          scene, so it needs an explicit way to close that isn't the toggle. */}
      {compact && open && <div class="stage-modelpick__backdrop" onClick={() => setOpen(false)} aria-hidden="true" />}
      {compact ? (
        <button
          class="stage-modelpick__chip"
          title={t('Switch the model for this chat — takes effect on your next message')}
          onClick={() => setOpen((o) => !o)}
        >
          <span class="stage-modelpick__cur-id">{chipLabel}</span>
          <span class="stage-modelpick__caret" aria-hidden="true">{open ? '▴' : '▾'}</span>
        </button>
      ) : (
        <div class="stage-modelpick__head">
          <span class="stage-modelpick__label">{t('Model')}</span>
          <button
            class="stage-modelpick__current"
            title={selecting ? t('Pick a model for this run') : t('Switch the model for this chat — takes effect on your next message')}
            onClick={() => setOpen((o) => !o)}
          >
            <span class="stage-modelpick__cur-id">{currentModel || defaultLabel || '—'}</span>
            {/* Which provider the CURRENT model came from — the collapsed button had
                the same ambiguity as a favorites row: a bare id does not say which
                backend is about to be billed. Only when there is a model to qualify.
                When nothing is overridden the same slot names the INHERITED model,
                so "Session model" stops being a dead end: the qualifier answers
                "which one?" at a glance, its tooltip adds the provider. */}
            {currentModel && currentProvider ? (
              <span class="stage-modelpick__cur-prov">{currentProvider}</span>
            ) : !currentModel && inherited ? (
              <span class="stage-modelpick__cur-prov" title={inheritedTitle}>
                {inherited.id}
              </span>
            ) : null}
            <span class="stage-modelpick__caret" aria-hidden="true">{open ? '▴' : '▾'}</span>
          </button>
        </div>
      )}
      {error && (
        <p class="stage-error" onClick={() => setError('')}>
          {error}
        </p>
      )}
      {open && (
        <div class="stage-modelpick__list">
          {selecting && (
            <div
              class={`stage-modelpick__row ${!currentModel ? 'stage-modelpick__row--current' : ''}`}
              onClick={() => {
                onSelect!('', '')
                setOpen(false)
              }}
            >
              <span class="stage-modelpick__id">{defaultLabel || t('Default')}</span>
              {/* The inherit row gets the same provenance a real row gets — id,
                  provider, billing — because choosing "inherit" over an explicit
                  pick is a comparison, and it cannot be made against a blank. */}
              {inherited && (
                <span class="stage-modelpick__prov" title={inheritedTitle}>
                  {inherited.id}
                  {inherited.provider ? ` · ${inherited.provider}` : ''}
                </span>
              )}
              {inherited && <AuthChip auth={inherited.auth} />}
            </div>
          )}
          {models.length === 0 && !error && <p class="stage-empty">{t('No models — check your provider login.')}</p>}
          {favorites.length > 0 && (
            <div class="stage-modelpick__group">
              <div class="stage-modelpick__provider">{t('★ Favorites')}</div>
              {favorites.map((m) => renderRow(m, 'fav:', true))}
            </div>
          )}
          {groups.map(([provider, ms]) => (
            <div key={provider} class="stage-modelpick__group">
              {/* The group heading carries the auth chip for every row beneath it,
                  so a metered provider reads as metered without repeating the chip
                  on each of its models. */}
              <div class="stage-modelpick__provider">
                <span>{provider}</span>
                <AuthChip auth={ms[0]?.auth} />
              </div>
              {ms.map((m) => renderRow(m, 'g:'))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
