import { tn } from '../i18n'

// The subject is the one thing a tool call is about: the file being edited, the
// command being run, the pattern being searched for. Stage 1 derives it with no
// per-tool knowledge at all, from an ordered list of argument keys.
//
// This sits in ui/ rather than beside the card in features/conversation/toolcard/
// because two surfaces derive a subject: the panel's card and the stage's single
// quiet line. An app may not import features/ (see boundaries.test.ts), and the
// alternative to promoting it is a second copy of the heuristic that drifts the
// first time stage 2 changes the first.
//
// The order is the point. `command` beats `path` because a bash call that cds
// somewhere is about the command, not the directory; `path` beats `name`
// because a file operation is about the file even when the call also carries a
// label. Stage 2 replaces this per tool and keeps it as the fallback, so the
// list only has to be right about the common case.
export const SUBJECT_KEYS = ['command', 'path', 'pattern', 'query', 'url', 'name', 'title', 'text'] as const

// A ceiling on what reaches the DOM. Nothing here is a layout decision. The
// header's width is CSS's business and this cap sits far past it. It exists
// because an argument can be a whole file's contents, and putting 400 KB into a
// header to then hide it with overflow is a real cost on every render.
const MAX_SUBJECT = 400

// How much of a basename is worth pinning. A path whose last segment is longer
// than this has a generated name, and holding all of it would push the part
// that identifies the directory off the row instead.
const MAX_TAIL = 32

export interface ToolSubject {
  // The whole subject. Goes in the title attribute, and is what tests assert on.
  text: string
  // The part CSS may elide when the header runs out of room.
  head: string
  // The part that must survive at any width, rendered beside the head at
  // flex:none. Empty when the meaningful end is the START of the string.
  tail: string
}

// toolSubject picks the subject out of a call's arguments.
//
// Returns null when there are no arguments at all, so the header renders the
// tool name alone rather than an empty slot. When there ARE arguments but none
// of the priority keys holds a usable string, it falls back to counting them:
// "3 arguments" says more than a blank does, and it is honest about the fact
// that this heuristic did not recognise the call.
export function toolSubject(args: unknown): ToolSubject | null {
  if (args == null || typeof args !== 'object' || Array.isArray(args)) return null
  const bag = args as Record<string, unknown>
  const keys = Object.keys(bag)
  if (keys.length === 0) return null

  for (const key of SUBJECT_KEYS) {
    const value = bag[key]
    if (typeof value !== 'string') continue
    // Whitespace is collapsed rather than preserved: a heredoc or a multi-line
    // command would otherwise put newlines into a row that must stay one line,
    // and HTML would render them as spaces anyway, unevenly.
    const text = clamp(value.replace(/\s+/g, ' ').trim())
    if (text === '') continue
    return split(text)
  }

  return { text: tn(keys.length, '%d argument', '%d arguments'), head: '', tail: '' }
}

// subjectOf builds a subject from text a caller already chose, applying the
// same whitespace, cap and head/tail rules the generic path applies. Stage 2's
// per-tool renderers use it so a bespoke subject elides exactly like a derived
// one, instead of each renderer inventing its own truncation.
export function subjectOf(text: string): ToolSubject | null {
  const clean = clamp(text.replace(/\s+/g, ' ').trim())
  if (clean === '') return null
  return split(clean)
}

// split decides which END of the subject has to survive a narrow header.
//
// For a path it is the basename: /home/dev/workspace/terva/packages/core/wire.go
// truncated from the right is every path in the repository. For anything else
// it is the beginning, because a command is identified by the program it runs,
// so the head is left to CSS's ordinary end-ellipsis and the tail stays empty.
//
// The split is what makes this responsive without measuring anything. A fixed
// character budget cannot work here: whatever JS decides, CSS still elides the
// result to the real width, and the basename goes with it.
function split(text: string): ToolSubject {
  const cut = text.lastIndexOf('/')
  if (cut < 0 || cut === text.length - 1) return { text, head: text, tail: '' }
  const base = text.slice(cut + 1)
  if (base.length > MAX_TAIL) return { text, head: text, tail: '' }
  return { text, head: text.slice(0, cut + 1), tail: base }
}

// clamp cuts an oversized subject in the MIDDLE, keeping both ends. Which end
// matters depends on the argument and this function cannot know, so it keeps
// both and loses what is least likely to be either.
export function clamp(s: string, max: number = MAX_SUBJECT): string {
  if (s.length <= max) return s
  const keep = max - 1
  const head = Math.ceil(keep / 2)
  return s.slice(0, head) + '…' + s.slice(s.length - (keep - head))
}
