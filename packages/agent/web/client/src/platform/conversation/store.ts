// The transcript model: a flat, ordered list of render items derived from the
// ctrlproto event stream (and the initial snapshot). Kept separate from the
// wire types so rendering has one stable shape to switch on.
import { t } from '../../i18n'
import type { WireEvent, WireMessage, WireBlock, WireAttachment, SharedFile } from '../ctrlproto/types'
import { isSafeImageMime, type ImageAttachment } from './images'

export type { ImageAttachment } from './images'
export { isSafeImageMime } from './images'

// Every item carries where it came from, which is what lets a snapshot MERGE into the
// list instead of rebuilding it:
//
//   idx      — the item's index in the LIVE transcript. Stable within an epoch: the
//              daemon's transcriptEpoch bumps only on a wholesale replace (compact,
//              /clear) and never on an append, so within one epoch an index never
//              moves. Absent for anything not in the live transcript.
//   history  — paged in from BEHIND a compaction (conversation.reveal). Not in the
//              live transcript at all, so it has no idx — and a snapshot must not
//              sweep it away, which is exactly what the merge protects.
//
//   sticky   — a row with no place in the transcript that must SURVIVE the merge
//              anyway. Exactly one row needs this and it is not a style choice:
//              a prompt an extension rejected produces no user bubble and no
//              reply, so without the row the message simply vanishes — and a
//              QUEUED prompt can be rejected while nobody is watching. It was
//              built as a plain notice, which mergeSnapshot drops by design, so
//              the one case its own comment names ("while nobody is watching")
//              lost its record at the very next turn-end snapshot.
//
// A notice has none of the three: it is ephemeral and is meant to be dropped on
// the next snapshot. That is the default, and `sticky` is the deliberate,
// type-visible exception rather than a comment claiming otherwise.
type Placed = { idx?: number; history?: boolean; sticky?: boolean }

export type Item = Placed &
  (
    // `directive` marks a [Direction] steer (Phase 6b / cast.speak): an
    // out-of-character instruction the user gave to move the story, run as a turn.
    // `text` is the instruction with the marker stripped; rendered as a
    // de-emphasized 🎬 note, not as the player's dialogue.
    // `time` is when the message was made, straight off the wire (RFC 3339): the
    // moment the user sent it, and for an assistant reply the moment its stream
    // finished — which is what "when did the answer arrive" means. Absent while a
    // reply is still streaming (it has not arrived yet) and from any daemon that
    // does not send it; the panel simply shows no stamp.
    | {
        kind: 'user'
        id: string
        text: string
        images?: ImageAttachment[]
        attachments?: WireAttachment[]
        // How many of this message's attachments were already swept when it was
        // sent. Rendered as a lapsed-files note, so a send whose files had all
        // expired does not look like one that carried nothing.
        attachmentsMissing?: number
        directive?: boolean
        time?: string
      }
    // directed/actor mark a line the user authored into the scene via directed
    // authorship (Phase 6) — a character's (actor set) or the narrator's (actor
    // empty) turn — rendered with 🎭 attribution rather than as a model reply.
    // routed marks a line the meta-narrator routed to a character (Worlds W3):
    // model-produced but likewise 🎭-attributed to its actor, not the main card.
    // `reasoning` is the thinking RECORDED on the finished message, which is a
    // different thing from `SessionState.reasoning`: that one is the live line
    // for the turn in flight and is dropped when the turn ends. This one is
    // transcript, so it survives a reload and a scroll away.
    | { kind: 'assistant'; id: string; text: string; streaming: boolean; images?: ImageAttachment[]; directed?: boolean; routed?: boolean; actor?: string; time?: string; reasoning?: string }
    // `shared` are files this call published for the user (share_file). They
    // hang off the tool item because that is where the wire puts them, but they
    // are NOT rendered here — sequenceConversationItems lifts them into rows of
    // their own, since a tool group renders collapsed and a download nobody can
    // see is the feature missing.
    | {
        kind: 'tool'
        id: string
        name: string
        args: unknown
        result?: string
        error?: boolean
        images?: ImageAttachment[]
        shared?: SharedFile[]
      }
    | { kind: 'error'; id: string; text: string }
  // host-injected (synthetic) user-role message, e.g. a continue-on-open-work
  // nudge — shown as a de-emphasized system note, not a user bubble.
  | { kind: 'system'; id: string; text: string }
  // one-shot host-originated notice (an extension command's display/error/insert
  // result), shown in-stream but never persisted — dropped on the next snapshot.
  // noticeKind is the wire Notice.kind (the item's own `kind` is the row
  // discriminator): typed notices like prompt_rebuilt can be filtered/styled;
  // unknown kinds fall back to the plain text.
    | { kind: 'notice'; id: string; level: string; ext?: string; text: string; noticeKind?: string }
  // a stuck-loop-hatch heads-up (a detector nudge or a model escalation, Go
  // EvStall / EvEscalation) — shown in-stream like a notice, ephemeral and
  // dropped on the next snapshot. tone picks the accent colour; glyph + text are
  // the rendered line, formatted once here so rendering stays a plain switch.
  // count is the coalesced nudge tally: repeated stalls update ONE item rather
  // than stacking, so a wedged run doesn't pile up notes.
    | { kind: 'hatch'; id: string; tone: 'accent' | 'ok' | 'err' | 'muted'; glyph: string; text: string; count?: number }
    // a compaction checkpoint: the turns above it are no longer in the model's
    // context, and `summary` is what stands in for them. Rendered as a divider with
    // the summary collapsed behind it — never as the user bubble its wire role
    // ('user') would otherwise produce.
    //
    // `ordinal` names the checkpoint for conversation.reveal (-1 = the latest, which
    // is always the one in the live transcript). `revealed` flips once the turns
    // above it have been paged in, so the control retires. Both are display state,
    // not wire state.
    | {
        kind: 'compaction'
        id: string
        summary: string
        tokensBefore?: number
        ordinal: number
        revealed?: boolean
      }
    // a /clear: the user said "done with that, start fresh". Nearer a session
    // boundary than a compaction, so the backward walk STOPS here and crossing it is
    // a separate, deliberate choice — never something scrolling does for you.
    | { kind: 'clear'; id: string; ordinal: number; revealed?: boolean }
  )

let seq = 0
const nextID = () => `i${++seq}`

// clip flattens a quoted stub to one line and caps it at max code points
// (not UTF-16 units, so an emoji is not cut in half), with an ellipsis when
// it cut anything.
const clip = (s: string, max: number): string => {
  const r = Array.from(s.trim().replace(/\s+/g, ' '))
  return r.length <= max ? r.join('') : r.slice(0, max).join('') + '…'
}

// directionBody detects the [Direction] steer convention (Phase 6b / cast.speak):
// an out-of-character direction the user gave, run as a turn. Returns the
// instruction with the marker stripped, or null for an ordinary message.
const DIRECTION_MARK = '[Direction]'
export function directionBody(text: string): string | null {
  return text.startsWith(DIRECTION_MARK) ? text.slice(DIRECTION_MARK.length).trim() : null
}

// userRow maps a user-role message to its item, splitting off a [Direction] steer
// as a de-emphasized directive row rather than the player's dialogue.
function userRow(
  text: string,
  images: ImageAttachment[] | undefined,
  id: string,
  placed: Placed,
  time?: string,
  attachments?: WireAttachment[],
  attachmentsMissing?: number,
): Item {
  const dir = directionBody(text)
  if (dir !== null) return { kind: 'user', id, text: dir, directive: true, time, ...placed }
  return { kind: 'user', id, text, images, attachments, attachmentsMissing, time, ...placed }
}

// msgID is a message's stable identity: (epoch, index). The daemon's transcriptEpoch
// bumps ONLY on a wholesale replace and never on an append, so an index never moves
// within an epoch — which is what makes a windowed snapshot mergeable and keeps a row
// from remounting every turn.
const msgID = (epoch: number, idx: number) => `m${epoch}.${idx}`

// The heading core.Compact puts on every summary. Stripped for display: the
// divider already says what this is, so the marker is noise inside it.
const SUMMARY_HEADING = '## Context Summary (compacted)'

function compactionItem(
  m: WireMessage,
  text: string,
  ordinal: number,
  id: string,
  placed: Placed,
): Extract<Item, { kind: 'compaction' }> & Placed {
  let summary = text
  if (summary.startsWith(SUMMARY_HEADING)) {
    summary = summary.slice(SUMMARY_HEADING.length).trimStart()
  }
  return { kind: 'compaction', id, summary, tokensBefore: m.tokens_before, ordinal, ...placed }
}

// Where a run of messages sits. A live-transcript window knows its epoch and the index
// its first message has; a span paged in from behind a compaction is `history` and has
// no index at all, because it is not in the live transcript.
export type Placement =
  | { history?: false; epoch: number; base: number }
  | { history: true; compactionOrdinal: number }

// blockText concatenates a message's text blocks.
//
// skipPreamble drops the FIRST text block, which is how a message carrying
// attachments hides the host's preamble — the block naming their absolute
// staging paths. The model needs it; a reader does not, and on a phone it wraps
// to nine lines above the two the user actually typed. The daemon says so with
// the message's own `preamble` flag, stamped where the block is prepended, so
// this is a contract rather than a guess about the text.
function blockText(blocks: WireBlock[] | undefined, skipPreamble = false): string {
  const texts = (blocks ?? []).filter((b) => b.type === 'text' && b.text).map((b) => b.text as string)
  return (skipPreamble ? texts.slice(1) : texts).join('')
}

// reasoningSummary pulls the readable thinking off a finished message.
//
// Empty is the ordinary case and must stay indistinguishable from absent: a
// display-only turn blanks the summary and leaves the block in place, so keying
// off the block's PRESENCE would hang a disclosure control on every assistant
// message with nothing behind it. Only text counts.
//
// A message can carry more than one block (one per assistant segment), and the
// providers separate sections with a blank line, so joining matches how the same
// text streams live.
function reasoningSummary(blocks: WireBlock[] | undefined): string | undefined {
  const parts = (blocks ?? [])
    .filter((b) => b.type === 'reasoning' && b.summary)
    .map((b) => b.summary as string)
  return parts.length ? parts.join('\n\n') : undefined
}

// imageAttachments pulls the renderable image blocks out of a content list —
// only those the carrier delivered with data (image-data negotiated); size-only
// blocks are skipped so the result is empty rather than broken <img> tags.
function imageAttachments(blocks: WireBlock[] | undefined): ImageAttachment[] | undefined {
  const out = (blocks ?? [])
    .filter((b) => b.type === 'image' && b.data && isSafeImageMime(b.mime_type ?? 'image/png'))
    .map((b) => ({ mime: b.mime_type ?? 'image/png', data: b.data as string }))
  return out.length ? out : undefined
}

// itemsFromMessages turns a run of wire messages into render items, attaching each
// tool_result to its tool_call by id, and PLACING each one so a later snapshot can
// merge rather than rebuild (see Placed).
//
// A live window carries {epoch, base}: item i is transcript index base+i, and its id
// is (epoch, index) — stable, so a re-broadcast neither remounts the row nor loses
// display state attached to it. A span paged in from behind a compaction carries
// {history: true}: it is not in the live transcript, has no index, and keeps a
// mint-once id.
//
// compactionOrdinal (history spans only) names the checkpoint the summary in the span
// belongs to; a live window's summary is always the LATEST checkpoint, hence -1.
export function itemsFromMessages(msgs: WireMessage[], at: Placement): Item[] {
  const out: Item[] = []
  const byCall = new Map<string, Extract<Item, { kind: 'tool' }> & Placed>()

  msgs.forEach((m, i) => {
    const live = at.history !== true
    const placed: Placed = live ? { idx: at.base + i } : { history: true }
    // A live message's identity is its position; a revealed one has none, so it mints.
    const id = live ? msgID(at.epoch, at.base + i) : nextID()
    const ordinal = live ? -1 : at.compactionOrdinal

    const text = blockText(m.content, !!m.preamble)
    const images = imageAttachments(m.content)
    const reasoning = reasoningSummary(m.content)
    // A message can be nothing BUT a preamble and a lapsed-file count: the user
    // attached a file, typed no words, and it expired before the send.
    //
    // reasoning earns its place in this guard: a turn that thinks and then calls
    // a tool carries thinking and a tool_call and NO text, which is the shape
    // Anthropic emits most. Without it that message makes no item and the
    // thinking behind every tool call is dropped on replay.
    if (text || images || reasoning || m.attachments?.length || m.attachments_missing) {
      out.push(
        // Checked before role: a compaction summary IS role 'user', and reading
        // the role first is exactly how it used to render as a user bubble.
        m.compaction
          ? compactionItem(m, text, ordinal, id, placed)
          : m.synthetic
            ? { kind: 'system', id, text, ...placed }
            : m.role === 'user'
              ? userRow(text, images, id, placed, m.time, m.attachments, m.attachments_missing)
              : { kind: 'assistant', id, text, streaming: false, images, reasoning, directed: m.directed, routed: m.routed, actor: m.actor, time: m.time, ...placed },
      )
    }
    for (const b of m.content ?? []) {
      if (b.type === 'tool_call' && b.id) {
        // A tool call is already stably identified by the provider's call id, so it
        // keeps it — but it still needs placing, or the merge cannot tell whether it
        // belongs above the window or inside it.
        const t: Extract<Item, { kind: 'tool' }> & Placed = {
          kind: 'tool',
          id: b.id,
          name: b.name ?? '',
          args: b.args,
          ...placed,
        }
        byCall.set(b.id, t)
        out.push(t)
      } else if (b.type === 'tool_result' && b.call_id) {
        const t = byCall.get(b.call_id)
        if (t) {
          t.result = blockText(b.content)
          t.error = b.is_error
          t.images = imageAttachments(b.content)
        }
      }
    }
    // Shares ride the tool-role MESSAGE, not its blocks, so they attach after
    // the block walk — each to the call it names. A share whose call is not in
    // this window (paged away above it) is dropped rather than orphaned: the
    // card belongs beside its row, and a row that is not here has no beside.
    for (const f of m.shared ?? []) {
      const t = f.call_id ? byCall.get(f.call_id) : undefined
      if (t) t.shared = [...(t.shared ?? []), f]
    }
  })
  return out
}

// Snapshot is what a windowed snapshot (or a conversation.history page) tells us about
// where its messages sit.
export type Window = { epoch: number; base: number; total: number; messages: WireMessage[] }

// mergeSnapshot folds a snapshot into the list the client already holds, instead of
// throwing that list away and rebuilding it.
//
// This is the whole point of stage 2, and it is what a stable identity buys. A snapshot
// arrives at the end of EVERY turn, not just on open. Rebuilding from it meant every
// row remounted, and anything the client held that the snapshot did not carry — the
// history you had paged in behind a compaction — silently vanished. Stage 1 had to
// keep a cache keyed on the divider's summary text just to put it back. That cache is
// now gone: the items were never lost in the first place.
//
// The rules:
//   - A DIFFERENT epoch means the transcript was wholesale replaced (compacted,
//     cleared). Everything the client holds describes a conversation that no longer
//     exists, revealed history included — so rebuild, which is the correct answer here
//     rather than a fallback.
//   - Otherwise keep everything ABOVE the window (idx < base), everything with no
//     place in the live transcript at all (history), and anything explicitly
//     STICKY, and swap in the window. Notices have none of the three and are
//     dropped, which is what they are for.
//   - Display state attached to a row that is still the same row survives: a compaction
//     divider you have already expanded must not come back offering to expand again,
//     or clicking it would splice its history in twice.
export function mergeSnapshot(prev: Item[], snap: Window, heldEpoch: number): Item[] {
  const rebuilt = itemsFromMessages(snap.messages, { epoch: snap.epoch, base: snap.base })
  if (heldEpoch !== snap.epoch) return rebuilt

  // Carry forward the display state of rows that are still the same rows.
  const before = new Map(prev.map((i) => [i.id, i]))
  const merged = rebuilt.map((i) => {
    const old = before.get(i.id)
    if (old?.kind === 'compaction' && i.kind === 'compaction') return { ...i, revealed: old.revealed }
    if (old?.kind === 'clear' && i.kind === 'clear') return { ...i, revealed: old.revealed }
    return i
  })

  const above = prev.filter((i) => i.history || i.sticky || (i.idx !== undefined && i.idx < snap.base))
  return [...above, ...merged]
}

// prependHistory puts a conversation.history page above what the client holds — the
// part of the LIVE transcript a window did not carry. Distinct from spliceRevealed,
// which goes behind a compaction to turns the model no longer has.
export function prependHistory(items: Item[], page: Window): Item[] {
  const older = itemsFromMessages(page.messages, { epoch: page.epoch, base: page.base })
  // Above the live transcript sits only revealed history, and this page belongs below
  // it — it IS live transcript. Splice in at the boundary rather than at index 0.
  const at = items.findIndex((i) => !i.history)
  if (at < 0) return [...items, ...older]
  return [...items.slice(0, at), ...older, ...items.slice(at)]
}

// spliceRevealed inserts the turns a compaction folded away directly ABOVE their
// divider, and retires that divider's "earlier turns" control.
//
// It can insert rather than merge because of a property the daemon guarantees and
// a Go test pins: the revealed span excludes the tail the checkpoint kept, so it
// never contains a message the client already has. No dedup, no reordering.
//
// `revealed` may itself begin with the PREVIOUS checkpoint's summary — a session
// compacted twice has a divider behind the divider — which itemsFromMessages turns
// into a fresh compaction item carrying prevOrdinal. Expanding one boundary simply
// exposes the next, and the walk terminates when the daemon reports prev_ordinal
// below zero (the beginning of the conversation).
// Divider is either boundary a user can page history back through.
export type Divider = Extract<Item, { kind: 'compaction' | 'clear' }>

// What one reveal came back with. prevClear means a /clear sits behind this
// checkpoint: the walk stops, and crossing is offered as its own choice.
export type RevealSpan = { messages: WireMessage[]; prevOrdinal: number; prevClear?: boolean }

// spliceRevealed inserts the turns a checkpoint folded away directly ABOVE its
// divider, and retires that divider's control.
//
// It can insert rather than merge because of a property the daemon guarantees and a
// Go test pins: the revealed span excludes the tail the checkpoint kept, so it never
// contains a message the client already has. No dedup, no reordering.
//
// Two things can appear above the inserted turns. The span may BEGIN with the
// previous checkpoint's summary — a session compacted twice has a divider behind the
// divider — which itemsFromMessages turns into a fresh compaction divider carrying
// prevOrdinal. Or prevClear says a /clear sits back there, which leaves no summary
// message to become one, so we mint the clear divider ourselves. Either way,
// expanding one boundary exposes the next, and the walk ends when the daemon reports
// prev_ordinal below zero.
export function spliceRevealed(items: Item[], dividerID: string, span: RevealSpan): Item[] {
  const at = items.findIndex((i) => i.id === dividerID)
  if (at < 0) return items // the divider went away under us (a snapshot landed) — drop the result
  const divider = items[at]
  if (divider.kind !== 'compaction' && divider.kind !== 'clear') return items

  const above: Item[] = itemsFromMessages(span.messages, {
    history: true,
    compactionOrdinal: span.prevOrdinal,
  })
  if (span.prevClear) {
    above.unshift({ kind: 'clear', id: nextID(), ordinal: span.prevOrdinal, history: true })
  }
  return [...items.slice(0, at), ...above, { ...divider, revealed: true }, ...items.slice(at + 1)]
}

// applyEvent folds one conversation event into the item list, returning a new
// array (immutable for Preact). Non-transcript events (permission/ask/usage/
// turn_end) are handled by the caller and ignored here.
//
// The rows it appends are OPTIMISTIC and unplaced: they have no idx, because the
// daemon has not told us where they land yet. That is deliberate, and it composes with
// mergeSnapshot — the snapshot at the end of the turn carries the same messages with
// their real indexes, and the merge drops the unplaced ones in favour of them. So the
// live turn paints immediately and settles onto the authoritative transcript, without
// either half having to know about the other.
// TRANSCRIPT_EVENTS names every event applyEvent turns into a transcript row.
//
// It is exported because the PACER needs the same answer: an event that becomes
// a row has a place relative to the streamed text around it and must wait behind
// whatever the pacer has not painted yet. pacer.ts used to keep its own
// hand-written copy of this list, and it had already fallen three events behind
// — stall, escalation and user_message_rejected jumped the jitter buffer, which
// split a streaming reply in two around a note describing it.
//
// Keep this in step with the switch below; a test parses the case labels out of
// this file and fails when the two disagree, so "keep in step" is not a request.
export const TRANSCRIPT_EVENTS: ReadonlySet<string> = new Set([
  'user_message',
  'text_delta',
  'assistant_message',
  'tool_call',
  'tool_result',
  'error',
  'notice',
  'user_message_rejected',
  'stall',
  'escalation',
])

export function applyEvent(items: Item[], ev: WireEvent): Item[] {
  switch (ev.type) {
    case 'user_message': {
      const text = blockText(ev.message?.content)
      const images = imageAttachments(ev.message?.content)
      if (!text && !images) return items
      if (ev.message?.synthetic) return [...items, { kind: 'system', id: nextID(), text }]
      const dir = directionBody(text)
      if (dir !== null) return [...items, { kind: 'user', id: nextID(), text: dir, directive: true }]
      return [...items, { kind: 'user', id: nextID(), text, images }]
    }
    case 'text_delta': {
      const last = items[items.length - 1]
      if (last && last.kind === 'assistant' && last.streaming) {
        const updated = { ...last, text: last.text + (ev.delta ?? '') }
        return [...items.slice(0, -1), updated]
      }
      return [...items, { kind: 'assistant', id: nextID(), text: ev.delta ?? '', streaming: true }]
    }
    case 'assistant_message': {
      // Images (agent-generated) ride the finalized message, never the deltas.
      const images = imageAttachments(ev.message?.content)
      // Recorded thinking rides the FINALIZED message, never the deltas — the
      // live line that streamed during the turn is a separate, ephemeral thing
      // (SessionState.reasoning) and is cleared as this arrives.
      const reasoning = reasoningSummary(ev.message?.content)
      const last = items[items.length - 1]
      if (last && last.kind === 'assistant' && last.streaming) {
        return [...items.slice(0, -1), { ...last, streaming: false, images: images ?? last.images, reasoning }]
      }
      const text = blockText(ev.message?.content)
      if (text || images || reasoning)
        return [...items, { kind: 'assistant', id: nextID(), text, streaming: false, images, reasoning }]
      return items
    }
    case 'tool_call':
      return [...items, { kind: 'tool', id: ev.id ?? nextID(), name: ev.name ?? '', args: ev.args }]
    case 'tool_result': {
      const idx = items.findIndex((it) => it.kind === 'tool' && it.id === ev.id)
      if (idx < 0) return items
      const t = items[idx] as Extract<Item, { kind: 'tool' }>
      const updated = {
        ...t,
        result: blockText(ev.content),
        error: ev.is_error,
        images: imageAttachments(ev.content),
        // The live half of what itemsFromMessages joins on replay. No call_id
        // check here: the event IS the call, and the daemon stamps them to match.
        shared: ev.shared?.length ? ev.shared : undefined,
      }
      return [...items.slice(0, idx), updated, ...items.slice(idx + 1)]
    }
    case 'error':
      return [...items, { kind: 'error', id: nextID(), text: ev.error ?? 'unknown error' }]
    case 'notice': {
      const n = ev.notice
      if (!n?.text) return items
      return [...items, { kind: 'notice', id: nextID(), level: n.level, ext: n.ext, text: n.text, noticeKind: n.kind }]
    }
    case 'user_message_rejected': {
      // An extension's intercept refused this prompt before it reached the
      // model: no user bubble, no reply — without a row here the message
      // simply vanishes, and a QUEUED one can be rejected while nobody is
      // watching. A durable transcript row, not a toast, for that reason.
      // Quote a stub of what was blocked (a reason with no words attached
      // stops meaning anything once the exact words are forgotten); the full
      // text rides ev.rejected, so restore-to-composer needs no wire change.
      const why = ev.text || t('an extension blocked it')
      const quote = clip(ev.rejected ?? '', 80)
      const text = quote
        ? t('message blocked: %s — “%s”', why, quote)
        : t('message blocked: %s', why)
      // sticky, because the comment above is only true if the row outlives the
      // snapshot that would otherwise sweep it.
      return [...items, { kind: 'notice', id: nextID(), level: 'error', text, sticky: true }]
    }
    case 'stall': {
      // The stuck-loop hatch acted. rung says what it did, and the three cases
      // are deliberately not one counter: 1–2 are things terva SAID to the model,
      // 3 is a call it refused to dispatch, 4 is the turn ending. Absent rung
      // reads as the nudge, which is also what an older daemon sends.
      const s = ev.stall
      if (!s) return items
      if ((s.rung ?? 1) >= 4) {
        // Terminal and once per turn: dedup rather than coalesce, and show the
        // reason core wrote — the turn stopped, and the user is owed why.
        const text = s.detail || t('loop not breaking — terva ended the turn')
        if (items.some((it) => it.kind === 'hatch' && it.glyph === '⊗' && it.text === text)) return items
        return [...items, { kind: 'hatch', id: nextID(), tone: 'err', glyph: '⊗', text }]
      }
      // Coalesce into ONE counting item per glyph — a wedged run fires many, and
      // a growing stack of identical notes is noise.
      const refused = s.rung === 3
      const glyph = refused ? '⊘' : '⟳'
      const prev = items.findIndex((it) => it.kind === 'hatch' && it.glyph === glyph)
      const count = prev >= 0 ? ((items[prev] as Extract<Item, { kind: 'hatch' }>).count ?? 1) + 1 : 1
      const id = prev >= 0 ? items[prev].id : nextID()
      const line: Item = {
        kind: 'hatch',
        id,
        tone: refused ? 'err' : 'accent',
        glyph,
        count,
        text: refused
          ? t('loop not breaking — refused to run %s %d× this turn (the call was not dispatched)', s.tool || '—', count)
          : t('loop detected — nudged the model %d× this turn to break out (latest tool: %s)', count, s.tool || '—'),
      }
      return prev >= 0 ? [...items.slice(0, prev), line, ...items.slice(prev + 1)] : [...items, line]
    }
    case 'escalation': {
      // Rung 3: a model swap resolved. Word and colour it by disposition. Rare
      // (once per turn), so dedup identical rather than coalesce.
      const e = ev.escalation
      if (!e) return items
      let tone: 'accent' | 'ok' | 'err' | 'muted' = 'muted'
      let glyph = '•'
      let text = t('escalation declined — staying on %s', e.from_model || '—')
      if (e.disposition === 'switched') {
        tone = 'ok'
        glyph = '⇗'
        text = t('escalated to %s (%s) to break the loop', e.to_model || '—', e.to_provider || '')
      } else if (e.disposition === 'failed') {
        tone = 'err'
        glyph = '⚠'
        text = t('escalation to %s failed: %s', e.to_model || '—', e.detail || '')
      }
      if (items.some((it) => it.kind === 'hatch' && it.text === text)) return items
      return [...items, { kind: 'hatch', id: nextID(), tone, glyph, text }]
    }
    default:
      return items
  }
}
