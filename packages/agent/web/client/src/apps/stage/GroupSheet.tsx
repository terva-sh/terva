import { useEffect, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import type { CardSummary, Group } from '../../platform/ctrlproto/types'

// A small fixed palette so a group can carry an at-a-glance colour without a
// full colour picker. '' is "no colour" (the leading swatch).
const GROUP_COLORS = ['', '#e06c76', '#d19a66', '#e5c07b', '#98c379', '#56b6c2', '#61afef', '#c678dd']

// GroupSheet opens one card group's contents: the cards in it (tap to inspect,
// ✕ to remove), a rename field and a colour swatch, a shortcut to filter the
// library to just this group, and delete. Membership is added from a card's own
// details sheet; here it is only ever removed, so the sheet stays a "what's in
// this group" view rather than a second card browser.
export function GroupSheet(props: {
  group: Group
  cards: CardSummary[]
  onOpenCard: (card: CardSummary) => void
  onRemoveCard: (cardId: string) => void
  onSave: (name: string, color: string) => void
  onDelete: () => void
  onFilter: () => void
  onClose: () => void
}) {
  const { group, cards } = props
  const [name, setName] = useState(group.name)
  const [color, setColor] = useState(group.color ?? '')

  // Re-seed the editable fields when a different group is opened (the parent
  // reuses this sheet for whichever chip was tapped).
  useEffect(() => {
    setName(group.name)
    setColor(group.color ?? '')
  }, [group.id])

  const dirty = name.trim() !== group.name || color !== (group.color ?? '')
  const canSave = !!name.trim() && dirty

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
          <button class="stage-groupsheet__filter" disabled={cards.length === 0} onClick={props.onFilter}>
            {t('Show only this group')}
          </button>
        </div>

        <div class="stage-groupsheet__count">
          {cards.length === 0 ? t('No cards yet — add cards from their ⋯ details.') : tn(cards.length, '%d card', '%d cards')}
        </div>

        <ul class="stage-groupsheet__cards">
          {cards.map((card) => (
            <li key={card.id} class="stage-groupsheet__card">
              <button class="stage-groupsheet__cardopen" onClick={() => props.onOpenCard(card)}>
                {card.avatar_url ? (
                  <img class="stage-groupsheet__avatar" src={card.avatar_url} alt="" />
                ) : (
                  <span class="stage-groupsheet__avatar stage-groupsheet__avatar--blank" aria-hidden="true">
                    {(card.name[0] ?? '·').toUpperCase()}
                  </span>
                )}
                <span class="stage-groupsheet__cardname">{card.name}</span>
              </button>
              <button class="stage-groupsheet__remove" title={t('Remove %s from this group', card.name)} onClick={() => props.onRemoveCard(card.id)}>
                ✕
              </button>
            </li>
          ))}
        </ul>

        <button class="stage-sheet__delete" onClick={props.onDelete}>
          {t('Delete group')}
        </button>
      </div>
    </div>
  )
}
