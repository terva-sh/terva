import { t, tn } from '../../i18n'
import type { Item } from '../../platform/conversation/store'
import type { WireAttachment } from '../../platform/ctrlproto/types'
import { CopyButton } from '../../ui/CopyButton'
import { clockTime, compact, humanBytes, localInstant, truncate } from '../../ui/formatting'
import { ImageGallery } from '../../ui/ImageGallery'
import { Markdown } from '../../ui/Markdown'
import { memo } from '../../ui/memo'
import { ClearDivider } from './ClearDivider'
import { CompactionDivider, type RevealFn } from './CompactionDivider'
import type { ToolView } from './types'

export type { ToolView } from './types'

// The markdown memo that used to live here now lives in ui/Markdown, so Stage
// gets it too — it had re-introduced the un-memoized render this comment was
// written about. See that file for the measurement and the reasoning.
// MessageTime is the arrival stamp under a bubble: a bare, muted time of day,
// with the full localized instant behind it for the cases the clock alone
// cannot settle — a session resumed days later, a transcript read after
// midnight. Renders nothing at all when the message has no wire time (still
// streaming, or an older daemon), so the row is unchanged rather than showing
// a blank.
export function MessageTime({ time }: { time?: string }) {
  const clock = clockTime(time)
  if (!clock) return null
  return (
    <span class="msg-time" title={localInstant(time)}>
      {clock}
    </span>
  )
}

// AttachedFiles labels what the user attached to a message.
//
// Deliberately inert — no link, no button, no download. The bytes live in a
// staging area the daemon sweeps on a TTL, so by the time anyone reads an old
// message the file is usually gone; an affordance that failed most of the time
// would be worse than none. (Sending files the other way — agent to user — is a
// separate flow and will not reuse this.)
//
// It renders instead of, not beside, the host preamble that names the staging
// paths: the store drops that block when the message's `preamble` flag is set.
//
// missing counts attachments that had already been swept when the message was
// sent, and is said out loud rather than omitted. A file can lapse between being
// dropped on the composer and the send — a phone tab left open overnight is
// enough — and a message that quietly showed nothing would leave the user
// reading an answer that ignores files they believe they attached.
function AttachedFiles({ files, missing }: { files: WireAttachment[]; missing?: number }) {
  return (
    <div class="msg-files">
      {files.map((f, i) => (
        <span key={i} class="msg-file" title={f.mime ? `${f.name} — ${f.mime}` : f.name}>
          <span class="msg-file__name">{f.name}</span>
          {f.size ? <span class="msg-file__size">{humanBytes(f.size)}</span> : null}
        </span>
      ))}
      {missing ? (
        <span class="msg-file msg-file--gone" title={t('Staged files are removed 24 hours after upload')}>
          {tn(missing, '%d attachment had expired', '%d attachments had expired')}
        </span>
      ) : null}
    </div>
  )
}

function AssistantMessage({ item }: { item: Extract<Item, { kind: 'assistant' }> }) {
  return (
    <div class="msg-wrap assistant-wrap">
      <div class="msg assistant md">
        <Markdown text={item.text} />
        {item.images && <ImageGallery images={item.images} />}
      </div>
      {item.text && <CopyButton text={item.text} />}
      <MessageTime time={item.time} />
    </div>
  )
}

// Memoized: the conversation list re-renders on every token delta, and without this
// every row in it re-rendered too — though only the one being streamed had changed.
// Rows are reference-stable during a turn, so shallow equality catches the rest.
export const MessageContent = memo(function MessageContent({
  item,
  toolView,
  onReveal,
  revealing,
}: {
  item: Item
  toolView: ToolView
  onReveal?: RevealFn
  revealing?: boolean
}) {
  switch (item.kind) {
    case 'user':
      return (
        <div class="msg-wrap user-wrap">
          <div class="msg user">
            {/* Coerced, not truthy-tested: both operands are numbers, and a bare
                `0 || 0` renders as a literal "0" in the bubble. */}
            {!!(item.attachments?.length || item.attachmentsMissing) && (
              <AttachedFiles files={item.attachments ?? []} missing={item.attachmentsMissing} />
            )}
            {item.text}
            {item.images && <ImageGallery images={item.images} />}
          </div>
          {item.text && <CopyButton text={item.text} />}
          <MessageTime time={item.time} />
        </div>
      )
    case 'assistant':
      // Stream as raw text (re-parsing markdown per token is wasteful); render
      // markdown once the message finalizes.
      if (item.streaming) {
        return <div class="msg assistant streaming">{item.text}</div>
      }
      return <AssistantMessage item={item} />
    case 'error':
      return <div class="msg err">{item.text}</div>
    case 'compaction':
      return <CompactionDivider item={item} onReveal={onReveal} revealing={revealing} />
    case 'clear':
      return <ClearDivider item={item} onReveal={onReveal} revealing={revealing} />
    case 'system':
      // Host-injected nudge (e.g. continue-on-open-work) — de-emphasized so it
      // doesn't read as something the user typed.
      return (
        <div class="sys-note">
          <span class="sys-tag">{t('auto')}</span>
          {item.text}
        </div>
      )
    case 'notice':
      // A one-shot result from an extension command (display/error). Attributed
      // to its extension, styled red when it's an error.
      return (
        <div class={`sys-note notice${item.level === 'error' ? ' err' : ''}`}>
          {item.ext && <span class="sys-tag">{item.ext}</span>}
          {item.text}
        </div>
      )
    case 'hatch':
      // The stuck-loop hatch acting in real time: a detector nudge or a model
      // escalation. Glyph + tone colour distinguish nudge / swap / fail / skip.
      return (
        <div class={`sys-note hatch ${item.tone}`}>
          <span class="hatch-glyph" aria-hidden="true">
            {item.glyph}
          </span>
          {item.text}
        </div>
      )
    case 'tool':
      if (toolView === 'hidden') return null
      if (toolView === 'minimal') {
        return (
          <div class="tool-min" aria-hidden="true">
            ▸ {item.name}
            {item.error ? ' · ' + t('failed') : ''}
          </div>
        )
      }
      return (
        <div class="tool">
          <div class="tool-head">
            <span class="tool-name">{item.name}</span>
            {item.args != null && <span class="tool-args">{compact(item.args)}</span>}
          </div>
          {item.result && <pre class={`tool-result${item.error ? ' err' : ''}`}>{truncate(item.result, 2000)}</pre>}
          {item.images && <ImageGallery images={item.images} />}
        </div>
      )
  }
})
