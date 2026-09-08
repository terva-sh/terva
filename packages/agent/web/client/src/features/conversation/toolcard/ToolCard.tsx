import type { ComponentChildren } from 'preact'
import { useMemo, useState } from 'preact/hooks'
import { t, tn } from '../../../i18n'
import type { Item } from '../../../platform/conversation/store'
import { humanBytes } from '../../../ui/formatting'
import { ImageGallery } from '../../../ui/ImageGallery'
import { presentTool } from './renderers'

export type ToolItem = Extract<Item, { kind: 'tool' }>

// How many lines of a result render before the card offers the rest. Twelve is
// enough for a short diff or a test summary to land whole, and short enough
// that three consecutive calls still fit on a screen.
const CLAMP_LINES = 12

// How much of a result reaches the DOM at all, expanded or not. The clamp above
// is about reading; this is about a 40 MB log costing 40 MB of nodes. Past it
// the card says how much more there is instead of rendering it.
const SOFT_CAP = 20_000

// Argument names whose values never render. Matched as a substring, so
// `github_token` and `api_key_id` are caught by one entry each.
//
// This exists because stage 1 stops truncating arguments. The 200-character cut
// was hiding secrets by accident, and accident is not a security property, but
// removing it without this would be a real regression: a tool called with a
// bearer token now puts it in the DOM in full.
const SECRET_KEY = /token|secret|password|passwd|credential|api[-_]?key|private[-_]?key|bearer|authorization/i

// The mask is a fixed width. A mask that tracked the value's length would leak
// the length, which for a credential is worth something to an attacker.
const MASK = '••••••••'

// ToolCard renders one tool call: what was called, what it was about, and what
// came back.
//
// Stage 1 of docs/proposals/web-tool-cards.md. It adds no per-tool knowledge:
// every tool renders through this one component, and the subject line is
// derived generically (see ui/toolSubject.ts). Stage 2 introduces a renderer
// table and keeps this as the fallback.
export function ToolCard({ item }: { item: ToolItem }) {
  const [argsOpen, setArgsOpen] = useState(false)
  const [expanded, setExpanded] = useState(false)

  // The transcript re-renders on every token delta, and a renderer parses a
  // diff or splits a command, so this is keyed on the inputs rather than run
  // per token for every visible card.
  const view = useMemo(
    () => presentTool(item.name, item.args, item.result ?? '', !!item.error),
    [item.name, item.args, item.result, item.error],
  )
  const subject = view.subject

  const body = useMemo(() => {
    // A per-tool renderer hands back one node per rendered line, so the clamp
    // slices the same way it slices plain text.
    if (view.body) return { rows: view.body, lines: null, total: view.body.length, dropped: 0 }
    const text = view.bodyText ?? item.result ?? ''
    const capped = text.length > SOFT_CAP ? text.slice(0, SOFT_CAP) : text
    const lines = capped.split('\n')
    return { rows: null, lines, total: lines.length, dropped: text.length - capped.length }
  }, [view, item.result])

  const clamped = body.total > CLAMP_LINES
  const cut = <T,>(xs: T[]) => (expanded || !clamped ? xs : xs.slice(0, CLAMP_LINES))
  // Newlines go BETWEEN the rows, never on them, so twelve rows measure as
  // twelve lines and a clamped body has no blank line hanging under it.
  const shown = body.rows
    ? cut(body.rows).flatMap((row, i) => (i === 0 ? [row] : ['\n', row]))
    : cut(body.lines!).join('\n')
  const hasBody = body.rows ? body.rows.length > 0 : body.lines!.join('') !== ''

  return (
    <div class={`tool-card${item.error ? ' tool-card--err' : ''}`}>
      <div class="tool-card__head">
        <span class="tool-card__name">{item.name}</span>
        {subject && (
          <span class="tool-card__subject" title={subject.text}>
            <span class="tool-card__subject-head">{subject.head}</span>
            {subject.tail && <span class="tool-card__subject-tail">{subject.tail}</span>}
          </span>
        )}
        {view.meta && <span class="tool-card__meta">{view.meta}</span>}
        {item.args != null && (
          <button
            type="button"
            class="tool-card__disclose"
            aria-expanded={argsOpen}
            onClick={() => setArgsOpen((v) => !v)}
          >
            {argsOpen ? t('hide args') : t('args')}
          </button>
        )}
        {/* A renderer's own chip wins: `exit 1` and `+12 −4` both say more than
            ok/failed does. No chip at all while the call is still in flight,
            because a "running" chip that never clears reads as a stuck call. */}
        {view.chip ? (
          <span class={`tool-card__chip tool-card__chip--${view.chip.tone}`}>{view.chip.text}</span>
        ) : (
          item.result != null && (
            <span class={`tool-card__chip${item.error ? ' tool-card__chip--err' : ' tool-card__chip--ok'}`}>
              {item.error ? t('failed') : t('ok')}
            </span>
          )
        )}
      </div>

      {argsOpen && item.args != null && <pre class="tool-card__args">{jsonNodes(item.args, 0)}</pre>}

      {hasBody && <pre class="tool-card__body">{shown}</pre>}

      {(clamped || body.dropped > 0) && (
        <div class="tool-card__foot">
          {clamped && (
            <button type="button" class="tool-card__more" onClick={() => setExpanded((v) => !v)}>
              {expanded ? t('show less') : tn(body.total, 'show all %d line', 'show all %d lines')}
            </button>
          )}
          {/* Only once the reader has reached the end of what we hold, because
              before that the clamp is the honest answer to "is there more". */}
          {expanded && body.dropped > 0 && (
            <span class="tool-card__rest">{t('… %s more', humanBytes(body.dropped))}</span>
          )}
        </div>
      )}

      {item.images && <ImageGallery images={item.images} />}
    </div>
  )
}

// jsonNodes pretty-prints a call's arguments with two-space indent, tinting
// keys and string values, and masking anything whose key looks like a secret.
//
// Built as nodes rather than by highlighting a JSON string with a regex: the
// string form has already lost which quotes opened a key and which sat inside a
// value, so a regex pass mis-tints any argument containing a quote, and there
// is no reliable point at which to mask.
function jsonNodes(value: unknown, depth: number, masked = false): ComponentChildren {
  const pad = '  '.repeat(depth + 1)
  const close = '  '.repeat(depth)

  if (masked) return <span class="tool-card__mask">"{MASK}"</span>
  // i18n-exempt: a JSON literal, the same token the wire carried, not prose.
  if (value === null) return <span class="tool-card__lit">null</span>
  if (typeof value === 'string') return <span class="tool-card__str">{JSON.stringify(value)}</span>
  if (typeof value === 'number' || typeof value === 'boolean') {
    return <span class="tool-card__lit">{String(value)}</span>
  }

  if (Array.isArray(value)) {
    if (value.length === 0) return <>[]</>
    const out: ComponentChildren[] = ['[\n']
    value.forEach((v, i) => {
      out.push(pad, jsonNodes(v, depth + 1), i < value.length - 1 ? ',\n' : '\n')
    })
    out.push(close, ']')
    return <>{out}</>
  }

  if (typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>)
    if (entries.length === 0) return <>{'{}'}</>
    const out: ComponentChildren[] = ['{\n']
    entries.forEach(([k, v], i) => {
      out.push(
        pad,
        <span class="tool-card__key">{JSON.stringify(k)}</span>,
        ': ',
        jsonNodes(v, depth + 1, SECRET_KEY.test(k)),
        i < entries.length - 1 ? ',\n' : '\n',
      )
    })
    out.push(close, '}')
    return <>{out}</>
  }

  // undefined, a function, a symbol: nothing JSON would carry, and nothing the
  // wire can deliver. Rendered as a literal rather than dropped, so an argument
  // never silently disappears from the view of what was sent.
  return <span class="tool-card__lit">{String(value)}</span>
}
