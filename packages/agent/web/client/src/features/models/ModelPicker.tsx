import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ModelInfo } from '../../platform/ctrlproto/types'
import { humanCount } from '../../ui/formatting'

// ModelPicker is the searchable model switcher: a filter box over favorites +
// per-provider groups, each row a click-to-switch with its own ★ toggle.
export function ModelPicker({
  groups,
  favorites,
  current,
  onSwitch,
  onToggleFavorite,
  onClose,
}: {
  groups: [string, ModelInfo[]][]
  favorites: ModelInfo[]
  current?: string
  onSwitch: (id: string, provider?: string) => void
  onToggleFavorite: (provider: string, id: string, on: boolean) => void
  onClose: () => void
}) {
  const [q, setQ] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)
  useEffect(() => searchRef.current?.focus(), [])
  const needle = q.trim().toLowerCase()
  const match = (model: ModelInfo) =>
    !needle || model.id.toLowerCase().includes(needle) || model.provider.toLowerCase().includes(needle)
  const favMatched = favorites.filter(match)
  const groupsMatched = groups
    .map(([provider, models]) => [provider, models.filter(match)] as [string, ModelInfo[]])
    .filter(([, models]) => models.length > 0)

  const row = (model: ModelInfo, keyPrefix: string) => (
    <div
      key={keyPrefix + model.provider + '/' + model.id}
      class={`pick-row${model.id === current ? ' current' : ''}`}
      onClick={() => onSwitch(model.id, model.provider)}
    >
      <button
        class="pick-star"
        title={model.favorite ? t('Unfavorite') : t('Favorite')}
        onClick={(event) => {
          event.stopPropagation()
          onToggleFavorite(model.provider, model.id, !model.favorite)
        }}
      >
        {model.favorite ? '★' : '☆'}
      </button>
      <span class="pick-id">{model.id}</span>
      <span class="pick-meta">
        {model.provider}
        {model.context_window ? ' · ' + humanCount(model.context_window) : ''}
      </span>
    </div>
  )

  return (
    <div class="modal-scrim" onClick={onClose}>
      <div class="modal picker" onClick={(event) => event.stopPropagation()}>
        <input
          class="pick-search"
          ref={searchRef}
          placeholder={t('Search models…')}
          value={q}
          onInput={(event) => setQ((event.target as HTMLInputElement).value)}
          onKeyDown={(event) => event.key === 'Escape' && onClose()}
        />
        <div class="pick-list">
          {favMatched.length > 0 && <div class="pick-group">★ {t('favorites')}</div>}
          {favMatched.map((model) => row(model, 'fav:'))}
          {groupsMatched.map(([provider, models]) => (
            <div key={'grp' + provider}>
              <div class="pick-group">{provider}</div>
              {models.map((model) => row(model, 'g:'))}
            </div>
          ))}
          {favMatched.length === 0 && groupsMatched.length === 0 && (
            <div class="pick-empty">{t('no models match “%s”', q)}</div>
          )}
        </div>
      </div>
    </div>
  )
}
