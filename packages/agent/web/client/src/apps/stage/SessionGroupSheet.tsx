import { useEffect, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import { relativeTime } from './format'
import type { CardSummary, Group, SessionInfo } from '../../platform/ctrlproto/types'

// A fixed palette, shared with the card GroupSheet — a group carries an
// at-a-glance colour without a full picker.
const GROUP_COLORS = ['', '#e06c76', '#d19a66', '#e5c07b', '#98c379', '#56b6c2', '#61afef', '#c678dd']

// SessionGroupSheet opens one chat group's contents. Sessions have no detail
// sheet to hang an "in groups" toggle off (a chat row opens the chat), so unlike
// cards, membership is curated from HERE: the chats already in the group (tap to
// open, ✕ to remove) and an "add chats" picker of the rest. Plus rename, a
// colour, a filter shortcut, and delete.
export function SessionGroupSheet(props: {
  group: Group
  members: SessionInfo[]
  candidates: SessionInfo[]
  cardById: Map<string, CardSummary>
  onOpenChat: (session: string) => void
  onToggle: (sessionId: string) => void
  onSave: (name: string, color: string) => void
  onDelete: () => void
  onFilter: () => void
  onClose: () => void
}) {
  const { group, members, candidates, cardById } = props
  const [name, setName] = useState(group.name)
  const [color, setColor] = useState(group.color ?? '')
  const [adding, setAdding] = useState(false)

  useEffect(() => {
    setName(group.name)
    setColor(group.color ?? '')
  }, [group.id])

  const dirty = name.trim() !== group.name || color !== (group.color ?? '')
  const canSave = !!name.trim() && dirty

  const title = (s: SessionInfo) => s.title || (s.card ? cardById.get(s.card)?.name : '') || t('Untitled')

  return (
    <div class="stage-sheet-backdrop" onClick={props.onClose}>
      <div class="stage-sheet stage-groupsheet" onClick={(e) => e.stopPropagation()}>
        <header class="stage-groupsheet__head">
          <input
            class="stage-groupsheet__name"
            value={name}
            placeholder={t('Group name')}
            onInput={(e) => setName((e.target as HTMLInputElement).value)}
          />
          <button class="stage-drawer__close" onClick={props.onClose}>
            ✕
          </button>
        </header>

        <div class="stage-groupsheet__colors">
          {GROUP_COLORS.map((c) => (
            <button
              key={c || 'none'}
              class={`stage-groupsheet__swatch ${color === c ? 'stage-groupsheet__swatch--on' : ''} ${c ? '' : 'stage-groupsheet__swatch--none'}`}
              style={c ? { background: c } : undefined}
              title={c || t('No colour')}
              aria-label={c || t('No colour')}
              onClick={() => setColor(c)}
            />
          ))}
        </div>

        <div class="stage-groupsheet__acts">
          <button class="stage-groupsheet__save" disabled={!canSave} onClick={() => props.onSave(name.trim(), color)}>
            {t('Save')}
          </button>
          <button class="stage-groupsheet__filter" disabled={members.length === 0} onClick={props.onFilter}>
            {t('Show only this group')}
          </button>
        </div>

        <div class="stage-groupsheet__count">
          {members.length === 0 ? t('No chats yet — add some below.') : tn(members.length, '%d chat', '%d chats')}
        </div>

        <ul class="stage-groupsheet__cards">
          {members.map((s) => (
            <li key={s.id} class="stage-groupsheet__card">
              <button class="stage-groupsheet__cardopen" onClick={() => props.onOpenChat(s.id)}>
                <span class="stage-groupsheet__cardname">{title(s)}</span>
                <span class="stage-groupsheet__when">{relativeTime(s.updated)}</span>
              </button>
              <button class="stage-groupsheet__remove" title={t('Remove from this group')} onClick={() => props.onToggle(s.id)}>
                ✕
              </button>
            </li>
          ))}
        </ul>

        {candidates.length > 0 && (
          <div class="stage-groupsheet__add">
            <button class="stage-groupsheet__addtoggle" onClick={() => setAdding((v) => !v)}>
              {adding ? '▾' : '▸'} {t('Add chats')}
            </button>
            {adding && (
              <ul class="stage-groupsheet__cards">
                {candidates.map((s) => (
                  <li key={s.id} class="stage-groupsheet__card">
                    <span class="stage-groupsheet__cardname">{title(s)}</span>
                    <button class="stage-groupsheet__addbtn" title={t('Add to this group')} onClick={() => props.onToggle(s.id)}>
                      +
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}

        <button class="stage-sheet__delete" onClick={props.onDelete}>
          {t('Delete group')}
        </button>
      </div>
    </div>
  )
}
