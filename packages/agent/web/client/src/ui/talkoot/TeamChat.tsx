import { t } from '../../i18n'
import type { ChatBridgeState, ChatHeld, ChatOutbound } from '../../platform/ctrlproto/types'

// clock writes an instant as the time of day on this device, as 07:30.
export function clock(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const two = (n: number) => String(n).padStart(2, '0')
  return `${two(d.getHours())}:${two(d.getMinutes())}`
}

// why says when a held message goes out. A message held for a reason goes
// out when quiet hours end. Any other waits for the brief.
function why(h: ChatHeld): string {
  switch (h.reason) {
    case 'finished':
      return t('finished work')
    case 'decision':
      return t('a decision')
    case 'watch':
      return t('a watched change')
    default:
      return t('for the brief')
  }
}

// Outbound shows what the bridge holds back from the chat, and when it goes
// out. The host decides what a person's chat hears and when, so the app is
// where a person sees what they have not been sent yet.
function Outbound({ out }: { out: ChatOutbound }) {
  const held = out.held ?? []
  const count = held.length + (out.more ?? 0)
  const brief = out.brief_on_reply
    ? t('The brief goes out when you next write in chat.')
    : out.next_brief
      ? t('The next brief goes out at %s.', clock(out.next_brief))
      : ''
  return (
    <div class="talkoot-outbound">
      {out.quiet && (
        <div class="talkoot-note">
          {out.quiet_ends
            ? t('Quiet hours until %s. A question, an approval, and a reply to you still go out.', clock(out.quiet_ends))
            : t('Quiet hours. A question, an approval, and a reply to you still go out.')}
        </div>
      )}
      {count > 0 && (
        <div class="talkoot-note">{count === 1 ? t('1 message held back from chat') : t('%d messages held back from chat', count)}</div>
      )}
      {held.length > 0 && (
        <ul class="talkoot-held">
          {held.map((h) => (
            <li key={`${h.at}${h.text}`}>
              <span class="talkoot-held-why">{why(h)}</span> {clock(h.at)} {h.from ? `${h.from}: ` : ''}
              {h.text}
            </li>
          ))}
        </ul>
      )}
      {brief && <div class="talkoot-note">{brief}</div>}
    </div>
  )
}

// TeamChat says that a chat bridge serves this team, and in what state. The
// bridge takes the person's messages from outside the app, so the app must
// show that it is on. User configuration turns it on, and nothing here does:
// the block has no control.
//
// `chat` comes from the team list and never from the team's own view. The
// list is read again when the bridge changes, and the view is not, so a view
// would go on showing a bridge that was disconnected.
export function TeamChat({ chat }: { chat?: ChatBridgeState }) {
  if (!chat) return null
  const via = chat.connector ?? ''
  let line: string
  switch (chat.state) {
    case 'connected':
      line = chat.username ? t('Chat bridge on: %s as @%s', via, chat.username) : t('Chat bridge on: %s', via)
      break
    case 'error':
      line = t('Chat bridge down: %s', via)
      break
    default:
      line = t('Chat bridge connecting: %s', via)
  }
  return (
    <div class="talkoot-chat" data-state={chat.state}>
      <div class="talkoot-note">{line}</div>
      {chat.state === 'connected' && <div class="talkoot-note">{t('Messages from the paired account enter this room as yours.')}</div>}
      {chat.state === 'error' && chat.error && <div class="talkoot-error">{chat.error}</div>}
      {chat.outbound && <Outbound out={chat.outbound} />}
    </div>
  )
}
