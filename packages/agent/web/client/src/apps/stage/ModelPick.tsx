import { useEffect, useState } from 'preact/hooks'
import type { Client } from '../../platform/ctrlproto/client'
import type { ModelInfo } from '../../platform/ctrlproto/types'

// Compact context-window label (200000 → "200k"). Stage keeps its own tiny
// helper rather than pull the panel's humanCount (and its i18n) across.
function ctxLabel(n?: number): string {
  if (!n) return ''
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n)
}

// ModelPick is the Stage-native model/provider switcher shown in the steering
// drawer's Session block. It lists the daemon's logged-in models grouped by
// provider (favorites floated to the top of each group), highlights the one this
// chat is on, and switches it LIVE with models.switch — effective on the next
// message, no session recreate. It reuses the panel's verbs
// (models.list/switch/favorite) but renders on Stage's own skin instead of
// importing the panel's picker + CSS. The current model tracks info.model from
// the snapshot (updated by the session_updated the daemon broadcasts on switch).
export function ModelPick(props: { client: Client; sessionId: string; currentProvider?: string; currentModel?: string }) {
  const { client, sessionId, currentProvider, currentModel } = props
  const [models, setModels] = useState<ModelInfo[]>([])
  const [open, setOpen] = useState(false)
  const [error, setError] = useState('')

  const load = () =>
    client
      .send<{ models: ModelInfo[] }>('models.list', {}, '')
      .then((r) => setModels(r.models ?? []))
      .catch((e: unknown) => setError(String(e)))

  // Lazily load the catalog the first time the picker is opened — a chat that
  // never touches the model list never pays for it.
  useEffect(() => {
    if (open && models.length === 0) void load()
  }, [open])

  // Group by provider; favorites float to the top of their group (stable sort
  // keeps the rest in catalog order), mirroring the panel + TUI ordering.
  const groups = (() => {
    const g = new Map<string, ModelInfo[]>()
    for (const m of models) {
      const arr = g.get(m.provider) ?? []
      arr.push(m)
      g.set(m.provider, arr)
    }
    for (const arr of g.values()) arr.sort((a, b) => (a.favorite === b.favorite ? 0 : a.favorite ? -1 : 1))
    return [...g.entries()]
  })()

  const isCurrent = (m: ModelInfo) => m.id === currentModel && m.provider === currentProvider

  // Fire-and-forget: the session_updated snapshot re-renders info.model, so we
  // just close. Session id rides the frame (the switch targets THIS session).
  const switchTo = (m: ModelInfo) => {
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

  return (
    <div class="stage-modelpick">
      <div class="stage-modelpick__head">
        <span class="stage-modelpick__label">Model</span>
        <button
          class="stage-modelpick__current"
          title="Switch the model for this chat — takes effect on your next message"
          onClick={() => setOpen((o) => !o)}
        >
          <span class="stage-modelpick__cur-id">{currentModel || '—'}</span>
          <span class="stage-modelpick__caret" aria-hidden="true">{open ? '▴' : '▾'}</span>
        </button>
      </div>
      {error && (
        <p class="stage-error" onClick={() => setError('')}>
          {error}
        </p>
      )}
      {open && (
        <div class="stage-modelpick__list">
          {models.length === 0 && !error && <p class="stage-empty">No models — check your provider login.</p>}
          {groups.map(([provider, ms]) => (
            <div key={provider} class="stage-modelpick__group">
              <div class="stage-modelpick__provider">{provider}</div>
              {ms.map((m) => (
                <div
                  key={`${m.provider}/${m.id}`}
                  class={`stage-modelpick__row ${isCurrent(m) ? 'stage-modelpick__row--current' : ''}`}
                  onClick={() => switchTo(m)}
                >
                  <button
                    class="stage-modelpick__star"
                    title={m.favorite ? 'Unfavorite' : 'Favorite'}
                    onClick={(e) => {
                      e.stopPropagation()
                      toggleFav(m, !m.favorite)
                    }}
                  >
                    {m.favorite ? '★' : '☆'}
                  </button>
                  <span class="stage-modelpick__id">{m.id}</span>
                  {m.context_window ? <span class="stage-modelpick__meta">{ctxLabel(m.context_window)}</span> : null}
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
