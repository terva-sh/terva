// splitCommand breaks a shell command at its TOP-LEVEL operators, so a chained
// command reads as the pipeline it is instead of as one long line.
//
// The property that matters is that this never alters the command. It decides
// where to insert line breaks and nothing else, so joining `op + raw` back
// together reproduces the input byte for byte. bashsplit.test.ts asserts that
// over every case in its table, including the ones where the segmentation
// itself is wrong (see the limitations below). A renderer that quietly rewrote
// a command would be worse than one that never split it.

export interface Segment {
  // The connector that introduced this segment: '' for the first, then one of
  // '&&', '||', ';;', ';', '|'. Rendered dimmed in the gutter.
  op: string
  // The text after the operator, verbatim, spacing included. This is the half
  // that makes the round trip exact, and it is not what you render.
  raw: string
  // raw, trimmed. This is what you render.
  text: string
}

// What the scanner tracks: single quotes, double quotes, backslash escapes,
// backticks and parenthesis depth. An operator inside any of those is part of
// somebody's argument, not a place to break the line.
//
// Two things it deliberately does NOT track, both of which mis-SEGMENT without
// ever mis-quoting, because the round trip holds either way:
//
//   - Brace groups. `{ a; b; }` splits at its inner `;`. Recognising `{` needs
//     word-boundary analysis, since `{` is only special as a command word, and
//     the cost of getting that subtly wrong is higher than the cost of an
//     over-eager break in a form that is rare in a tool call.
//   - Heredoc bodies. `cat <<EOF ... EOF` splits on an operator inside the
//     body. Tracking it means tracking the delimiter, quoted and unquoted
//     forms, and `<<-` stripping.
//
// Both render as extra line breaks in the transcript. Neither changes a byte.
export function splitCommand(command: string): Segment[] {
  const segments: Segment[] = []
  let op = ''
  let start = 0
  let depth = 0
  let single = false
  let double = false
  let backtick = false

  const push = (end: number) => {
    const raw = command.slice(start, end)
    segments.push({ op, raw, text: raw.trim() })
  }

  for (let i = 0; i < command.length; i++) {
    const c = command[i]
    // Single quotes first: nothing escapes inside them, not even a backslash.
    if (single) {
      if (c === "'") single = false
      continue
    }
    if (c === '\\') {
      i++
      continue
    }
    if (double) {
      if (c === '"') double = false
      continue
    }
    if (backtick) {
      if (c === '`') backtick = false
      continue
    }
    if (c === "'") {
      single = true
      continue
    }
    if (c === '"') {
      double = true
      continue
    }
    if (c === '`') {
      backtick = true
      continue
    }
    if (c === '(') {
      depth++
      continue
    }
    if (c === ')') {
      if (depth > 0) depth--
      continue
    }
    if (depth > 0) continue

    const two = command.slice(i, i + 2)
    // `&&` and `||` are matched before `|`, and `;;` before `;`, or the
    // two-character forms break into two empty segments.
    let found = ''
    if (two === '&&' || two === '||' || two === ';;') found = two
    else if (c === '|') found = '|'
    else if (c === ';') found = ';'
    // A lone `&` backgrounds a command rather than joining two, so it is not a
    // break: `sleep 1 &` is one command and reads as one.
    if (!found) continue

    push(i)
    op = found
    i += found.length - 1
    start = i + 1
  }
  push(command.length)
  return segments
}

// joinSegments is the inverse, and exists so the round-trip property can be
// asserted directly rather than reconstructed by each test.
export function joinSegments(segments: Segment[]): string {
  return segments.map((s) => s.op + s.raw).join('')
}
