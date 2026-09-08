import type { ComponentChildren } from 'preact'
import type { ToolDisplay } from '../../../platform/ctrlproto/types'
import { subjectOf, toolSubject } from '../../../ui/toolSubject'
import {
  diffBody,
  hasRenderer,
  lines,
  presentTool,
  type ToolCall,
  type ToolPresentation,
} from './renderers'

// Stage 3 of docs/proposals/web-tool-cards.md: an extension declares how its
// tool's calls are drawn, and this turns that declaration into the same
// ToolPresentation stage 2's built-in renderers produce.
//
// The rule the whole file exists to keep: the hint is DATA. Nothing here
// evaluates a string, builds markup from one, or dispatches to anything the
// extension named that this file does not already contain. A template is
// substituted as text, and `body` selects from a table defined below. An
// extension can choose among the renderers this client has; it cannot supply
// one, and it cannot reach the DOM.

// ToolHints is what tools.display returns: hints by tool name, for the tools
// that declared one. Fetched once per session, so a holder can keep one
// reference and every memoized row below it stays memoized.
export type ToolHints = Record<string, ToolDisplay>

// BODIES is the closed set, and it is the whole set: a name outside it falls
// back to text. It deliberately omits the proposal's "code". The card body is
// already monospace with pre-wrap, so a code renderer would either be a second
// name for text, or would need white-space:pre and bring back the horizontal
// scroll well stage 1 removed. Shipping a name that does nothing is worse than
// not shipping it, and adding one later is additive.
const BODIES = ['text', 'json', 'diff', 'table'] as const
export type BodyKind = (typeof BODIES)[number]

export function isBodyKind(s: string): s is BodyKind {
  return (BODIES as readonly string[]).includes(s)
}

// A placeholder is {key} where key is a bare identifier. Anything else stays
// literal, so a subject containing JSON or a brace-quoted word survives intact
// instead of turning into a hole.
const PLACEHOLDER = /\{([A-Za-z_][A-Za-z0-9_]*)\}/g

// A cell wider than this is not a column, it is a paragraph. Bound the padding
// so one long value cannot push every other column off the card.
const MAX_CELL = 32
const MAX_COLUMNS = 8

// fillTemplate substitutes top-level argument values into the subject
// template. Only primitives substitute: an object or an array renders empty,
// because a nested structure flattened into a header says nothing a reader can
// use, and stringifying one is how a template becomes a way to dump an
// argument the card meant to redact.
//
// A key the arguments do not carry renders empty too. A hint is never a reason
// for a card to fail, so every path here produces a string.
export function fillTemplate(template: string, args: unknown): string {
  const bag =
    args != null && typeof args === 'object' && !Array.isArray(args)
      ? (args as Record<string, unknown>)
      : {}
  return template.replace(PLACEHOLDER, (_whole, key: string) => {
    const v = bag[key]
    if (typeof v === 'string') return v
    if (typeof v === 'number' || typeof v === 'boolean') return String(v)
    return ''
  })
}

// jsonBody pretty-prints a result that parses as JSON. A result that does not
// parse falls back to its own lines: an extension that declared json and
// returned a stack trace should still show the stack trace.
function jsonBody(text: string): ComponentChildren[] {
  try {
    const parsed: unknown = JSON.parse(text)
    return lines(JSON.stringify(parsed, null, 2))
  } catch {
    return lines(text)
  }
}

// tableBody aligns tab-separated output into columns.
//
// Tabs, and only tabs. Guessing columns from runs of spaces turns any prose
// result into a lopsided grid, and a tool that means to emit a table can emit
// tabs. A result with no tab in it falls back to plain lines rather than
// drawing one column of nothing.
//
// Alignment is space padding rather than CSS, because the body is monospace
// and the card measures its height in lines: padded text stays one node per
// line, so stage 1's clamp keeps working unchanged.
function tableBody(text: string): ComponentChildren[] {
  const rows = lines(text).map((line) => line.split('\t').slice(0, MAX_COLUMNS))
  if (!rows.some((r) => r.length > 1)) return lines(text)

  const widths: number[] = []
  for (const row of rows) {
    row.forEach((cell, i) => {
      widths[i] = Math.min(Math.max(widths[i] ?? 0, cell.length), MAX_CELL)
    })
  }
  return rows.map((row) =>
    row
      .map((cell, i) => (i === row.length - 1 ? cell : cell.padEnd(widths[i] ?? 0)))
      .join('  ')
      .trimEnd(),
  )
}

// applyDisplay renders a call through an extension's hint.
//
// Every field is independent and every one is optional, so a hint that names
// only a subject leaves the body alone, and a hint whose subject template
// yields nothing falls back to the generic argument-guessing subject rather
// than to an empty header.
export function applyDisplay(hint: ToolDisplay, call: ToolCall): ToolPresentation {
  const out: ToolPresentation = { subject: toolSubject(call.args) }

  if (hint.subject) {
    const filled = fillTemplate(hint.subject, call.args)
    const subject = subjectOf(filled)
    if (subject) out.subject = subject
  }

  const body = hint.body ?? ''
  if (body === 'diff') out.body = diffBody(call.result)
  else if (body === 'json') out.body = jsonBody(call.result)
  else if (body === 'table') out.body = tableBody(call.result)
  // 'text', absent, and any name this client does not know all leave the body
  // unset, which is the card's own plain rendering of the result.

  return out
}

// presentToolWithHint is the card's single entry point, and the only place
// that decides between a built-in renderer and an extension's hint.
//
// A built-in wins. An extension that registers a tool named `bash` must not be
// able to change how bash's cards are drawn, and the precedence is what makes
// that true rather than a promise that nobody registers such a name.
//
// A hint that throws falls back the same way a built-in renderer that throws
// does: to the generic subject. Nothing an extension declares takes the
// transcript down.
export function presentToolWithHint(
  name: string,
  args: unknown,
  result: string,
  error: boolean,
  hint?: ToolDisplay,
): ToolPresentation {
  if (hint && !hasRenderer(name)) {
    try {
      return applyDisplay(hint, { args, result, error })
    } catch {
      /* fall through to the generic presentation */
    }
  }
  return presentTool(name, args, result, error)
}

// redactedKeys returns the argument key names this hint asks the card to mask.
//
// It ADDS to the client's built-in denylist and cannot shrink it: an extension
// may protect a field named something the denylist would never guess, and may
// not unprotect one named `token`. Compared case-insensitively, because an
// argument named `API_KEY` and a hint naming `api_key` mean the same field.
export function redactedKeys(hint: ToolDisplay | undefined): Set<string> {
  const out = new Set<string>()
  for (const k of hint?.redact ?? []) out.add(k.toLowerCase())
  return out
}
