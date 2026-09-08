import type { ComponentChildren } from 'preact'
import { t, tn } from '../../../i18n'
import { subjectOf, toolSubject, type ToolSubject } from '../../../ui/toolSubject'
import { splitCommand } from './bashsplit'

// Stage 2 of docs/proposals/web-tool-cards.md: what each built-in tool looks
// like, keyed by tool name.
//
// The constraint that shapes every entry below: a renderer has exactly two
// inputs, the call's ARGUMENTS (structured JSON) and the RESULT TEXT (a
// string). Everything the tools compute into ToolResult.Details stops at the
// wire (core/tool.go:33), so `edit` cannot be asked how many edits it made and
// `bash` cannot be asked for its exit code. Both are recovered here, the first
// from args.edits.length and the second from the footer bash prints.
//
// A tool with no entry falls back to the generic subject and a plain body.
// That fallback is not a degraded path: every MCP tool, every extension tool,
// and deliver_result all land on it by construction, because none of them has
// a statically knowable schema. It stays first-class.

export interface Chip {
  text: string
  tone: 'ok' | 'err' | 'plain'
}

export interface ToolPresentation {
  subject: ToolSubject | null
  // A short muted note between the subject and the chip: "2 edits", "lines 40-59".
  meta?: string
  // Overrides the card's default ok/failed chip.
  chip?: Chip
  // One node per RENDERED LINE, with NO trailing newline of its own: the card
  // puts the newlines BETWEEN the rows it shows. An array rather than a
  // fragment because stage 1's 12-line clamp has to be able to slice it, and a
  // fragment would force either a nested scroll well or no clamp at all.
  //
  // The newline placement is not a detail. A row that carried its own would
  // leave a blank line under every clamped body, and would make twelve rows
  // measure as thirteen lines.
  body?: ComponentChildren[]
  // Plain text to render instead of the result, for a renderer whose body is
  // an ARGUMENT rather than output (memory's text, swarm_spawn's brief).
  bodyText?: string
}

export interface ToolCall {
  args: unknown
  result: string
  error: boolean
}

export type ToolRenderer = (call: ToolCall) => ToolPresentation

// ---------------------------------------------------------------------------
// helpers

function bag(args: unknown): Record<string, unknown> {
  return args != null && typeof args === 'object' && !Array.isArray(args)
    ? (args as Record<string, unknown>)
    : {}
}

function str(args: unknown, key: string): string | undefined {
  const v = bag(args)[key]
  return typeof v === 'string' && v.trim() !== '' ? v : undefined
}

function num(args: unknown, key: string): number | undefined {
  const v = bag(args)[key]
  return typeof v === 'number' ? v : undefined
}

function arr(args: unknown, key: string): unknown[] | undefined {
  const v = bag(args)[key]
  return Array.isArray(v) ? v : undefined
}

function lines(text: string): string[] {
  return text === '' ? [] : text.split('\n')
}

// EXIT_FOOTER matches what tools/bash.go prints at the end of every run. The
// number is in ToolResult.Details as well, and Details does not cross the wire,
// so parsing the text is the only way a browser can know it.
//
// Anchored to the END of the result, not to any line, because bash prints this
// last and output can contain anything. With /m a program that printed the
// string "[exit 1]" would rewrite the chip of a run that succeeded.
const EXIT_FOOTER = /\n?\[exit (\d+)\](?:\s+Took\s+(.+?))?\s*$/

// DIFF_LINE ignores the ---/+++ file headers, which are not changes.
function diffStat(text: string): { added: number; removed: number } | null {
  let added = 0
  let removed = 0
  let sawHunk = false
  for (const line of lines(text)) {
    if (line.startsWith('@@')) sawHunk = true
    else if (line.startsWith('+++') || line.startsWith('---')) continue
    else if (line.startsWith('+')) added++
    else if (line.startsWith('-')) removed++
  }
  if (added === 0 && removed === 0 && !sawHunk) return null
  return { added, removed }
}

// diffBody colours a unified diff per line. The whole card is monospace
// already, so this adds colour and nothing else.
function diffBody(text: string): ComponentChildren[] {
  return lines(text).map((line, i) => {
    let cls = 'tc-diff'
    if (line.startsWith('@@')) cls = 'tc-diff tc-diff--hunk'
    else if (line.startsWith('+++') || line.startsWith('---')) cls = 'tc-diff tc-diff--file'
    else if (line.startsWith('+')) cls = 'tc-diff tc-diff--add'
    else if (line.startsWith('-')) cls = 'tc-diff tc-diff--del'
    return (
      <span key={i} class={cls}>
        {line}
      </span>
    )
  })
}

// MATCH_LINE is the shape grep and glob results come back in. The path is
// non-greedy up to the first colon-number-colon, so a path containing a colon
// still parses.
const MATCH_LINE = /^(.*?):(\d+):(.*)$/

// matchBody groups `path:line:text` under one heading per file, so a result
// spanning four files reads as four groups rather than forty repetitions of
// the same prefix.
function matchBody(text: string): ComponentChildren[] {
  const out: ComponentChildren[] = []
  let current = ''
  lines(text).forEach((line, i) => {
    const m = MATCH_LINE.exec(line)
    if (!m) {
      out.push(
        <span key={`p${i}`} class="tc-match-plain">
          {line}
        </span>,
      )
      return
    }
    const [, path, no, rest] = m
    if (path !== current) {
      current = path
      out.push(
        <span key={`f${i}`} class="tc-match-file">
          {path}
        </span>,
      )
    }
    out.push(
      <span key={`m${i}`} class="tc-match-row">
        <span class="tc-match-no">{no.padStart(6)}</span>
        {'  '}
        {rest}
      </span>,
    )
  })
  return out
}

// listBody renders one item per line with a bullet, for results that are a
// list of paths or titles rather than prose.
function listBody(items: string[]): ComponentChildren[] {
  return items.map((item, i) => (
    <span key={i} class="tc-list-row">
      {'· '}
      {item}
    </span>
  ))
}

// ---------------------------------------------------------------------------
// the built-in table

const bashRenderer: ToolRenderer = ({ args, result, error }) => {
  const command = str(args, 'command') ?? ''
  const footer = EXIT_FOOTER.exec(result)
  const code = footer ? Number(footer[1]) : undefined
  // The footer is lifted into the header, so the body does not repeat it.
  const transcript = footer ? result.replace(EXIT_FOOTER, '').replace(/\n+$/, '') : result

  const rows: ComponentChildren[] = []
  // A command with no top-level operator is already one line and reads exactly
  // as it does today, so it gets no gutter and no extra block.
  const segments = splitCommand(command)
  if (segments.length > 1) {
    segments.forEach((s, i) => {
      rows.push(
        <span key={`c${i}`} class="tc-cmd-row">
          <span class="tc-cmd-op">{s.op}</span>
          {' '}
          {s.text}
        </span>,
      )
    })
  }
  lines(transcript).forEach((line, i) => {
    rows.push(
      <span key={`t${i}`} class={error ? 'tc-fail' : undefined}>
        {line}
      </span>,
    )
  })

  return {
    subject: subjectOf(command),
    meta: footer?.[2],
    chip:
      code === undefined
        ? undefined
        : { text: t('exit %s', String(code)), tone: code === 0 ? 'ok' : 'err' },
    body: rows.length > 0 ? rows : undefined,
  }
}

const editRenderer: ToolRenderer = ({ args, result }) => {
  const edits = arr(args, 'edits')
  const stat = diffStat(result)
  return {
    subject: subjectOf(str(args, 'path') ?? ''),
    // The count comes from the arguments because the result is a bare diff
    // with no summary line in it. There is nowhere else it could come from.
    meta: edits ? tn(edits.length, '%d edit', '%d edits') : undefined,
    chip: stat ? { text: `+${stat.added} \u2212${stat.removed}`, tone: 'plain' } : undefined,
    body: diffBody(result),
  }
}

const writeRenderer: ToolRenderer = ({ args }) => {
  const content = str(args, 'content')
  const n = content ? lines(content).length : undefined
  return {
    subject: subjectOf(str(args, 'path') ?? ''),
    meta: n === undefined ? undefined : tn(n, '%d line', '%d lines'),
  }
}

const readRenderer: ToolRenderer = ({ args }) => {
  const offset = num(args, 'offset')
  const limit = num(args, 'limit')
  let meta: string | undefined
  if (offset !== undefined && limit !== undefined) meta = t('lines %s', `${offset}\u2013${offset + limit - 1}`)
  else if (offset !== undefined) meta = t('from line %s', String(offset))
  else if (limit !== undefined) meta = tn(limit, '%d line', '%d lines')
  return { subject: subjectOf(str(args, 'path') ?? ''), meta }
}

const globRenderer: ToolRenderer = ({ args, result }) => ({
  subject: subjectOf(str(args, 'pattern') ?? ''),
  meta: str(args, 'path'),
  body: listBody(lines(result).filter((l) => l.trim() !== '')),
})

// grep's own arguments include a field called `glob`, and `glob` is separately
// a tool. Keying the registry on the TOOL NAME and reading fields only inside
// the matching entry is what keeps those two apart.
const grepRenderer: ToolRenderer = ({ args, result }) => {
  const hits = lines(result).filter((l) => MATCH_LINE.test(l)).length
  return {
    subject: subjectOf(str(args, 'pattern') ?? ''),
    meta: str(args, 'path') ?? str(args, 'glob'),
    chip: hits > 0 ? { text: tn(hits, '%d match', '%d matches'), tone: 'plain' } : undefined,
    body: matchBody(result),
  }
}

const taskCreateRenderer: ToolRenderer = ({ args }) => {
  const tasks = arr(args, 'tasks') ?? []
  const titles = tasks.map((task) => str(task, 'title') ?? '').filter((s) => s !== '')
  const first = titles[0] ?? ''
  const extra = titles.length - 1
  return {
    subject: subjectOf(extra > 0 ? `${first} ${tn(extra, '+%d more', '+%d more')}` : first),
    body: titles.length > 0 ? listBody(titles) : undefined,
  }
}

const taskUpdateRenderer: ToolRenderer = ({ args }) => {
  const id = str(args, 'id') ?? ''
  const status = str(args, 'status')
  const evidence = str(args, 'evidence')
  const note = str(args, 'note')
  return {
    subject: subjectOf(status ? `${id} \u2192 ${status}` : id),
    // What the agent claimed, which is the part worth reading back. The tool's
    // own result is a confirmation line and says less.
    bodyText: evidence || note ? [evidence, note].filter(Boolean).join('\n\n') : undefined,
  }
}

const swarmSpawnRenderer: ToolRenderer = ({ args }) => {
  const who = str(args, 'persona') ?? str(args, 'tier')
  const task = str(args, 'task') ?? ''
  const first = task.split('\n')[0] ?? ''
  return {
    subject: subjectOf(who ? `${who}: ${first}` : first),
    // The brief is often thousands of words. The card's own clamp handles the
    // height; this only decides what the body IS.
    bodyText: task !== '' ? task : undefined,
  }
}

const memoryRenderer: ToolRenderer = ({ args }) => {
  const action = str(args, 'action') ?? ''
  const scope = str(args, 'scope')
  const what = str(args, 'name') ?? str(args, 'match')
  const head = scope ? `${action} (${scope})` : action
  return {
    subject: subjectOf(what ? `${head} ${what}` : head),
    bodyText: str(args, 'text'),
  }
}

const askRenderer: ToolRenderer = ({ args }) => {
  const one = str(args, 'question')
  const many = arr(args, 'questions')
  const subject = one
    ? subjectOf(one)
    : many
      ? subjectOf(tn(many.length, '%d question', '%d questions'))
      : null
  const body: ComponentChildren[] = []
  const render = (q: unknown, i: number) => {
    const text = str(q, 'question') ?? ''
    const options = (arr(q, 'options') ?? []).map((o) => String(o))
    body.push(
      <span key={`q${i}`} class="tc-ask-q">
        {text}
      </span>,
    )
    options.forEach((o, k) => {
      body.push(
        <span key={`o${i}-${k}`} class="tc-list-row">
          {'· '}
          {o}
        </span>,
      )
    })
  }
  if (many) many.forEach(render)
  else if (one) render(args, 0)
  return { subject, body: body.length > 0 ? body : undefined }
}

export const RENDERERS: Record<string, ToolRenderer> = {
  bash: bashRenderer,
  edit: editRenderer,
  write: writeRenderer,
  read: readRenderer,
  glob: globRenderer,
  grep: grepRenderer,
  task_create: taskCreateRenderer,
  task_update: taskUpdateRenderer,
  swarm_spawn: swarmSpawnRenderer,
  memory: memoryRenderer,
  ask_user_question: askRenderer,
}

// presentTool is the single entry point the card calls. An unknown tool, or a
// renderer that throws on a shape it did not expect, falls back to the generic
// subject and a plain body rather than taking the transcript down with it.
export function presentTool(name: string, args: unknown, result: string, error: boolean): ToolPresentation {
  const renderer = RENDERERS[name]
  if (!renderer) return { subject: toolSubject(args) }
  try {
    return renderer({ args, result, error })
  } catch {
    return { subject: toolSubject(args) }
  }
}
