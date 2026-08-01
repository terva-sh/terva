import { useEffect, useRef, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ModelInfo } from '../../platform/ctrlproto/types'
import { humanCount } from '../../ui/formatting'
import { renamedTo } from './label'

// ModelPicker is the searchable model switcher: a filter box over favorites +
// per-provider groups, each row a click-to-switch with its own ★ toggle and a
// ◉ set-as-default toggle (the TUI picker's ctrl+d, which arms the same
// project/global choice).
export function ModelPicker({
  groups,
  favorites,
  current,
  onSwitch,
  onToggleFavorite,
  onSetDefault,
  onEdit,
  onClose,
}: {
  groups: [string, ModelInfo[]][]
  favorites: ModelInfo[]
  current?: string
  onSwitch: (id: string, provider?: string) => void
  onToggleFavorite: (provider: string, id: string, on: boolean) => void
  onSetDefault: (provider: string, id: string, scope: 'global' | 'project') => void
  // Opens the per-model overrides (context window, max tokens, …). They are
  // settings ABOUT this model, so they hang off its row rather than off a pane
  // that would have to list every model a second time.
  onEdit: (provider: string, id: string) => void
  onClose: () => void
}) {
  const [q, setQ] = useState('')
  // The model whose scope prompt is armed, mirroring the TUI's two-key confirm:
  // choosing a default is a persistent, cross-session write, so it does not
  // happen on a single tap. Held as a key, and the prompt renders once in the
  // footer — a model appears in both the ★ group and its provider group, so an
  // inline prompt would render twice for the same choice.
  const [promoting, setPromoting] = useState<ModelInfo | null>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  useEffect(() => searchRef.current?.focus(), [])
  const needle = q.trim().toLowerCase()
  // A renamed model has to stay findable by BOTH spellings: the operator
  // searches for the name they gave it, someone reading a log searches the id.
  const match = (model: ModelInfo) =>
    !needle ||
    model.id.toLowerCase().includes(needle) ||
    model.provider.toLowerCase().includes(needle) ||
    (!!model.renamed && (model.display_name ?? '').toLowerCase().includes(needle))
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
      <button
        class={`pick-default${model.default ? ' on' : ''}`}
        title={
          model.default
            ? model.default_scope === 'project'
              ? t('Default for new sessions (this project)')
              : t('Default for new sessions (everywhere)')
            : t('Set as default for new sessions')
        }
        onClick={(event) => {
          event.stopPropagation()
          setPromoting(model)
        }}
      >
        {model.default ? '◉' : '○'}
      </button>
      <button
        class="pick-default"
        title={t('Model settings (context window, max tokens, …)')}
        onClick={(event) => {
          event.stopPropagation()
          onEdit(model.provider, model.id)
        }}
      >
        ⚙
      </button>
      <span class="pick-id">{renamedTo(model) ?? model.id}</span>
      {/* The id follows the name rather than being replaced by it: it is what
          the operator renamed away from, and the only spelling that appears in
          logs, sessions, and --model. It ellipsizes when the row is tight, so
          the title carries it in full — the tail of a local id (:Q4_K_XL) is
          the quantization, not decoration. */}
      <span class="pick-meta" title={renamedTo(model) ? model.id : undefined}>
        {model.provider}
        {model.context_window ? ' · ' + humanCount(model.context_window) : ''}
        {/* Last, so that when the row is tight the ellipsis eats the id rather
            than the context window: the id is recoverable from the title, the
            window is not shown anywhere else on the row. */}
        {renamedTo(model) ? ' · ' + model.id : ''}
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
        {promoting && (
          <div class="pick-promote">
            <span class="pick-promote-q">{t('set “%s” as the default for new sessions:', promoting.id)}</span>
            <div class="pick-promote-btns">
              <button
                class="btn sm primary"
                onClick={() => {
                  onSetDefault(promoting.provider, promoting.id, 'global')
                  setPromoting(null)
                }}
              >
                {t('Everywhere')}
              </button>
              <button
                class="btn sm"
                title={t('Only in this workspace, and only while it is trusted')}
                onClick={() => {
                  onSetDefault(promoting.provider, promoting.id, 'project')
                  setPromoting(null)
                }}
              >
                {t('This project')}
              </button>
              <button class="btn sm" onClick={() => setPromoting(null)}>
                {t('Cancel')}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
