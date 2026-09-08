import { describe, expect, it } from 'vitest'
import { clamp, toolSubject } from './toolSubject'

describe('toolSubject', () => {
  it('takes the first priority key that holds a string', () => {
    // command outranks path: a bash call that cds somewhere is about what it
    // then runs, not about the directory it ran in.
    expect(toolSubject({ path: '/tmp/x', command: 'just test' })?.text).toBe('just test')
    expect(toolSubject({ pattern: 'lines_added', path: 'packages/' })?.text).toBe('packages/')
  })

  it('skips a key whose value is not a usable string', () => {
    // A tool that takes `path: string[]` must not render "[object Object]" or
    // crash; it falls through to the next key.
    expect(toolSubject({ path: ['a', 'b'], query: 'wire format' })?.text).toBe('wire format')
    expect(toolSubject({ command: 42, name: 'build' })?.text).toBe('build')
    expect(toolSubject({ path: '   ', name: 'build' })?.text).toBe('build')
  })

  it('collapses whitespace so a heredoc cannot break the row', () => {
    const s = toolSubject({ command: 'cat <<EOF\n  one\n  two\nEOF' })
    expect(s?.text).toBe('cat <<EOF one two EOF')
    expect(s?.text).not.toContain('\n')
  })

  it('pins the basename of a path so a narrow header cannot cut it off', () => {
    const s = toolSubject({ path: 'packages/agent/web/client/src/styles.css' })
    // The tail is what CSS renders at flex:none; the head is what it may elide.
    expect(s?.tail).toBe('styles.css')
    expect(s?.head).toBe('packages/agent/web/client/src/')
    expect(s!.head + s!.tail).toBe(s!.text)
  })

  it('leaves a command whole, because its meaningful end is the start', () => {
    const s = toolSubject({ command: 'just test-unit' })
    expect(s?.tail).toBe('')
    expect(s?.head).toBe('just test-unit')
  })

  it('does not pin a basename long enough to push the path off the row', () => {
    const long = 'a'.repeat(40)
    const s = toolSubject({ path: `dir/${long}` })
    expect(s?.tail).toBe('')
  })

  it('does not pin an empty tail when the path ends in a separator', () => {
    const s = toolSubject({ path: 'packages/core/' })
    expect(s?.tail).toBe('')
    expect(s?.text).toBe('packages/core/')
  })

  it('counts the arguments when it recognises none of them', () => {
    // Says what it does know rather than rendering a blank slot, and admits
    // the heuristic missed.
    expect(toolSubject({ alpha: 1, beta: 2, gamma: 3 })?.text).toBe('3 arguments')
    expect(toolSubject({ alpha: 1 })?.text).toBe('1 argument')
  })

  it('renders nothing at all when there are no arguments', () => {
    // The header then shows the tool name alone, not an empty slot.
    expect(toolSubject({})).toBeNull()
    expect(toolSubject(null)).toBeNull()
    expect(toolSubject(undefined)).toBeNull()
    expect(toolSubject('a string')).toBeNull()
    expect(toolSubject(['an', 'array'])).toBeNull()
  })

  it('caps a subject that carries a whole file', () => {
    const s = toolSubject({ text: 'x'.repeat(5000) })
    expect(s!.text.length).toBeLessThanOrEqual(400)
  })
})

describe('clamp', () => {
  it('keeps both ends, because which one matters is not knowable here', () => {
    expect(clamp('abcdefghij', 5)).toBe('ab…ij')
    expect(clamp('abcdefghij', 10)).toBe('abcdefghij')
    expect(clamp('short', 100)).toBe('short')
  })

  it('never returns more than the budget', () => {
    for (const n of [2, 3, 7, 20]) {
      expect(clamp('y'.repeat(500), n).length).toBeLessThanOrEqual(n)
    }
  })
})
