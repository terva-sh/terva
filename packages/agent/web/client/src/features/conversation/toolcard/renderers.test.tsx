// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render } from '@testing-library/preact'
import { presentTool } from './renderers'

afterEach(cleanup)

// body renders a presentation's body and hands back its text, which is what
// almost every assertion here is about. The classes are covered where they
// carry meaning (a diff line, a match heading) rather than everywhere.
const body = (name: string, args: unknown, result = '', error = false) => {
  const p = presentTool(name, args, result, error)
  const { container } = render(<>{p.body ?? p.bodyText}</>)
  return { p, container, text: container.textContent ?? '' }
}

describe('bash', () => {
  it('lifts the exit footer into the chip and out of the transcript', () => {
    const { p, text } = body('bash', { command: 'just test' }, 'ok\n\n[exit 0]  Took 1.2s')
    expect(p.chip).toEqual({ text: 'exit 0', tone: 'ok' })
    expect(p.meta).toBe('1.2s')
    // The footer is in the header now, so repeating it in the body would be
    // the same fact twice.
    expect(text).not.toContain('[exit 0]')
    expect(text).toContain('ok')
  })

  it('marks a non-zero exit as a failure', () => {
    const { p } = body('bash', { command: 'just check' }, 'boom\n[exit 1]')
    expect(p.chip).toEqual({ text: 'exit 1', tone: 'err' })
  })

  it('reads the footer only at the end, so output cannot forge it', () => {
    // A command that prints the string mid-stream must not rewrite the chip of
    // a run that actually succeeded.
    const { p, text } = body('bash', { command: 'cat log' }, '[exit 1]\nstill going\n[exit 0]  Took 2s')
    expect(p.chip).toEqual({ text: 'exit 0', tone: 'ok' })
    // ...and the forged line stays in the transcript, because it is output.
    expect(text).toContain('[exit 1]')
  })

  it('has no chip when there is no footer at all', () => {
    // A still-running call, or a daemon that changed the format. The card's
    // own ok/failed chip takes over rather than this inventing one.
    const { p } = body('bash', { command: 'sleep 1' }, 'no footer here')
    expect(p.chip).toBeUndefined()
  })

  it('segments a chained command and leaves a simple one alone', () => {
    const chained = body('bash', { command: 'cd x && make | tee log' }, '[exit 0]')
    expect(chained.container.querySelectorAll('.tc-cmd-row')).toHaveLength(3)
    expect(chained.text).toContain('&&')
    expect(chained.text).toContain('|')

    const simple = body('bash', { command: 'just test' }, '[exit 0]')
    expect(simple.container.querySelectorAll('.tc-cmd-row')).toHaveLength(0)
  })
})

describe('edit', () => {
  const diff = ['--- a/x.go', '+++ b/x.go', '@@ -1,3 +1,4 @@', ' ctx', '-old', '+new', '+extra'].join('\n')

  it('counts the edits from the arguments, because the result has no summary', () => {
    const { p } = body('edit', { path: 'src/styles.css', edits: [{}, {}] }, diff)
    expect(p.meta).toBe('2 edits')
    expect(p.subject?.tail).toBe('styles.css')
  })

  it('derives the chip from the diff itself, not from the wire', () => {
    // lines_added/lines_removed exist only on the live event, so a chip built
    // from them would vanish on the next snapshot. The diff is in the result
    // text in both paths.
    const { p } = body('edit', { path: 'x.go', edits: [{}] }, diff)
    expect(p.chip).toEqual({ text: '+2 \u22121', tone: 'plain' })
  })

  it('does not count the ---/+++ file headers as changes', () => {
    const { p } = body('edit', { path: 'x.go' }, '--- a\n+++ b\n@@ -0,0 +1 @@\n+one')
    expect(p.chip?.text).toBe('+1 \u22120')
  })

  it('colours the diff per line', () => {
    const { container } = body('edit', { path: 'x.go' }, diff)
    expect(container.querySelectorAll('.tc-diff--add')).toHaveLength(2)
    expect(container.querySelectorAll('.tc-diff--del')).toHaveLength(1)
    expect(container.querySelectorAll('.tc-diff--hunk')).toHaveLength(1)
  })

  it('has no chip when the result is not a diff', () => {
    const { p } = body('edit', { path: 'x.go' }, 'wrote the file')
    expect(p.chip).toBeUndefined()
  })
})

describe('the rest of the table', () => {
  it('write counts the lines it was given', () => {
    const { p } = body('write', { path: 'a/b.txt', content: 'one\ntwo\nthree' }, 'ok')
    expect(p.meta).toBe('3 lines')
    expect(p.subject?.tail).toBe('b.txt')
  })

  it('read names the range it asked for', () => {
    expect(presentTool('read', { path: 'x', offset: 40, limit: 20 }, '', false).meta).toBe('lines 40\u201359')
    expect(presentTool('read', { path: 'x', offset: 40 }, '', false).meta).toBe('from line 40')
    expect(presentTool('read', { path: 'x' }, '', false).meta).toBeUndefined()
  })

  it('grep groups matches by file and counts them', () => {
    const result = ['a/x.go:12:hit one', 'a/x.go:40:hit two', 'b/y.go:3:hit three'].join('\n')
    const { p, container } = body('grep', { pattern: 'hit', path: 'a/' }, result)
    expect(p.chip).toEqual({ text: '3 matches', tone: 'plain' })
    expect(p.meta).toBe('a/')
    // Two files, so two headings, not three repetitions of the prefix.
    expect(container.querySelectorAll('.tc-match-file')).toHaveLength(2)
    expect(container.querySelectorAll('.tc-match-row')).toHaveLength(3)
  })

  it('grep reads its own `glob` argument and is not confused with the glob TOOL', () => {
    // grep takes a field called `glob`; `glob` is also a tool. The registry is
    // keyed on the tool name, so the two cannot cross.
    expect(presentTool('grep', { pattern: 'p', glob: '**/*.go' }, '', false).meta).toBe('**/*.go')
    expect(presentTool('glob', { pattern: '**/*.go' }, 'a.go\nb.go', false).subject?.text).toBe('**/*.go')
  })

  it('task_create names the first title and counts the rest', () => {
    const tasks = [{ title: 'Add the parser' }, { title: 'Wire it up' }, { title: 'Test it' }]
    const { p, container } = body('task_create', { tasks }, 'created 3')
    expect(p.subject?.text).toBe('Add the parser +2 more')
    expect(container.querySelectorAll('.tc-list-row')).toHaveLength(3)
  })

  it('task_update names the transition', () => {
    const { p, text } = body('task_update', { id: 'task-7', status: 'done', evidence: '12/12 green' }, 'ok')
    expect(p.subject?.text).toBe('task-7 \u2192 done')
    expect(text).toContain('12/12 green')
  })

  it('memory names the action and its scope', () => {
    const { p } = body('memory', { action: 'add', scope: 'project', text: 'a fact' }, 'saved')
    expect(p.subject?.text).toBe('add (project)')
  })

  it('ask_user_question renders the options under each question', () => {
    const { p, container } = body('ask_user_question', {
      questions: [{ question: 'Which base?', options: ['trunk', 'stage 1'] }],
    })
    expect(p.subject?.text).toBe('1 question')
    expect(container.querySelectorAll('.tc-ask-q')).toHaveLength(1)
    expect(container.querySelectorAll('.tc-list-row')).toHaveLength(2)
  })
})

describe('the fallback', () => {
  it('is what an unknown tool gets, and it is not degraded', () => {
    // Every MCP tool, every extension tool and deliver_result land here by
    // construction: none of them has a schema known at build time.
    const p = presentTool('some_mcp_tool', { query: 'weather in Helsinki' }, 'sunny', false)
    expect(p.subject?.text).toBe('weather in Helsinki')
    expect(p.body).toBeUndefined()
  })

  it('survives a known tool called with a shape it did not expect', () => {
    // The six tools that accept {} are the ordinary case here, but the point
    // is broader: a renderer must never take the transcript down with it.
    for (const name of ['bash', 'edit', 'grep', 'read', 'write', 'task_create', 'ask_user_question']) {
      expect(() => presentTool(name, {}, '', false)).not.toThrow()
      expect(() => presentTool(name, null, '', false)).not.toThrow()
      expect(() => presentTool(name, { path: 42, edits: 'not an array' }, '', false)).not.toThrow()
    }
  })

  it('renders a call with no arguments as the bare tool name', () => {
    expect(presentTool('terva_status', {}, 'ok', false).subject).toBeNull()
    expect(presentTool('bash', {}, 'ok', false).subject).toBeNull()
  })
})
