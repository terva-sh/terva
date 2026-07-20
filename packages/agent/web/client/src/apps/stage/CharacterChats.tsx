import type { CardSummary, SessionInfo } from '../../platform/ctrlproto/types'
import { relativeTime } from './format'
import { t, tn } from '../../i18n'

// CharacterChats is the resume-or-start sheet (rough-edge #2): tapping a
// character that ALREADY has chats lands here — its existing conversations,
// most-recent first (the daemon lists by mtime), each one tap to resume — with
// "New chat" to begin a fresh one and "Details" to open the full card. A
// character with NO chats skips this sheet and starts a chat directly, so a
// first encounter stays frictionless (that branch lives in Library).
export function CharacterChats(props: {
  card: CardSummary
  chats: SessionInfo[]
  busy: boolean
  onClose: () => void
  onOpen: (session: string) => void
  onNew: () => void
  onDetails: () => void
  onDelete: (session: SessionInfo) => void
}) {
  const { card, chats, busy, onClose, onOpen, onNew, onDetails, onDelete } = props
  return (
    <div class="stage-sheet-backdrop" onClick={onClose}>
      <div class="stage-sheet stage-sheet--chats" onClick={(e) => e.stopPropagation()}>
        <header class="stage-charchats__head">
          {card.avatar_url ? (
            <img class="stage-cardsheet__avatar" src={card.avatar_url} alt="" />
          ) : (
            <div class="stage-cardsheet__avatar stage-cardsheet__avatar--blank" aria-hidden="true">
              {(card.name[0] ?? '·').toUpperCase()}
            </div>
          )}
          <div class="stage-cardsheet__id">
            <h3>{card.name}</h3>
            <div class="stage-cardsheet__meta">
              <span>
                {tn(chats.length, '%d chat', '%d chats')}
              </span>
              <button class="stage-charchats__details" title={t('Inspect the full character card')} onClick={onDetails}>
                {t('Details')}
              </button>
            </div>
          </div>
          <button class="stage-drawer__close" onClick={onClose}>
            ✕
          </button>
        </header>

        <button class="stage-sheet__start" disabled={busy} onClick={onNew}>
          {t('+ New chat')}
        </button>

        <ul class="stage-charchats__list">
          {chats.map((s) => (
            <li key={s.id} class="stage-charchats__row">
              <button class="stage-charchats__item" onClick={() => onOpen(s.id)}>
                <span class="stage-charchats__title">{s.title || t('Untitled')}</span>
                <span class="stage-charchats__sub">
                  {s.experience === 'play' && <span class="stage-charchats__exp">{t('play')}</span>}
                  {(s.messages ?? 0) > 0 && (
                    <span>
                      {tn(s.messages ?? 0, '%d message', '%d messages')}
                    </span>
                  )}
                  {relativeTime(s.updated) && <span class="stage-charchats__when">{relativeTime(s.updated)}</span>}
                </span>
              </button>
              <button class="stage-charchats__del" title={t('Delete this chat')} aria-label={t('Delete this chat')} onClick={() => onDelete(s)}>
                🗑
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}
