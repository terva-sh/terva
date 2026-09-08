import { describe, expect, it } from 'vitest'
import { joinSegments, splitCommand, type Segment } from './bashsplit'

// Every command this suite touches, including the ones the splitter segments
// WRONG. The round-trip property has to hold over all of them, because that is
// the property that says a renderer cannot alter what was run.
const CORPUS = [
  'just test',
  '',
  '   ',
  'a && b',
  'a||b',
  'a | b | c',
  'a; b ;c',
  'case x in a) y ;; esac',
  'sleep 1 &',
  'echo "a && b"',
  "echo 'a | b'",
  'echo a\\&\\&b',
  'echo "he said \\"a && b\\""',
  '(cd x && make) | tee log',
  'echo `date && hostname`',
  'find . -name "*.go" | xargs grep -l "x" | head -3',
  'cd /home/dev/workspace/terva && python3 -m py_compile scripts/release-delta.py && just release-delta | head -14',
  '{ a; b; }',
  'cat <<EOF\na | b\nEOF',
  'x=1 y="a;b" ./run --flag="p|q"',
  '|',
  '&&',
  'a &&',
  '&& b',
]

const ops = (segs: Segment[]) => segs.map((s) => s.op)
const texts = (segs: Segment[]) => segs.map((s) => s.text)

describe('splitCommand round trip', () => {
  // The one property that must never break. A renderer that rewrote a command
  // while displaying it would be worse than one that never split at all.
  it('reproduces the input byte for byte over the whole corpus', () => {
    for (const cmd of CORPUS) {
      expect(joinSegments(splitCommand(cmd)), `round trip failed for: ${cmd}`).toBe(cmd)
    }
  })

  it('reproduces inputs built from random operator soup', () => {
    // Cheap fuzz over the characters that drive the scanner's state machine.
    const alphabet = ['a', ' ', '&&', '||', '|', ';', "'", '"', '\\', '(', ')', '`', ';;']
    let seed = 12345
    const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff)
    for (let n = 0; n < 500; n++) {
      let cmd = ''
      for (let k = 0; k < 12; k++) cmd += alphabet[Math.floor(rnd() * alphabet.length)]
      expect(joinSegments(splitCommand(cmd)), `round trip failed for: ${cmd}`).toBe(cmd)
    }
  })
})

describe('splitCommand segmentation', () => {
  it('leaves a command with no top-level operator as one segment', () => {
    const segs = splitCommand('just test-unit')
    expect(segs).toHaveLength(1)
    expect(segs[0].op).toBe('')
    expect(segs[0].text).toBe('just test-unit')
  })

  it('cuts at each kind of top-level operator', () => {
    expect(ops(splitCommand('a && b'))).toEqual(['', '&&'])
    expect(ops(splitCommand('a || b'))).toEqual(['', '||'])
    expect(ops(splitCommand('a | b'))).toEqual(['', '|'])
    expect(ops(splitCommand('a ; b'))).toEqual(['', ';'])
    // ';;' is one operator, not two: matched before ';' so it cannot produce
    // an empty segment between the halves.
    expect(ops(splitCommand('a ;; b'))).toEqual(['', ';;'])
  })

  it('reads a real chained command as its pipeline', () => {
    const segs = splitCommand(
      'cd /home/dev/workspace/terva && python3 -m py_compile scripts/release-delta.py && just release-delta | head -14',
    )
    expect(ops(segs)).toEqual(['', '&&', '&&', '|'])
    expect(texts(segs)).toEqual([
      'cd /home/dev/workspace/terva',
      'python3 -m py_compile scripts/release-delta.py',
      'just release-delta',
      'head -14',
    ])
  })

  it('does not cut inside quotes, escapes, subshells or backticks', () => {
    expect(splitCommand('echo "a && b"')).toHaveLength(1)
    expect(splitCommand("echo 'a | b'")).toHaveLength(1)
    expect(splitCommand('echo a\\&\\&b')).toHaveLength(1)
    // A backslash escapes inside double quotes too, so the closing quote here
    // is the LAST one and the operator stays inside the string.
    expect(splitCommand('echo "he said \\"a && b\\""')).toHaveLength(1)
    expect(splitCommand('echo `date && hostname`')).toHaveLength(1)
    // The subshell is one stage of the pipeline; its inner && is not a break.
    expect(texts(splitCommand('(cd x && make) | tee log'))).toEqual(['(cd x && make)', 'tee log'])
  })

  it('does not treat a lone & as a join', () => {
    // `sleep 1 &` backgrounds one command; it does not chain two.
    expect(splitCommand('sleep 1 &')).toHaveLength(1)
  })

  it('keeps an operator with nothing on one side rather than dropping it', () => {
    // Malformed input still round-trips, and still shows the operator that was
    // typed, because the transcript records what ran and not what should have.
    expect(ops(splitCommand('a &&'))).toEqual(['', '&&'])
    expect(texts(splitCommand('a &&'))).toEqual(['a', ''])
    expect(ops(splitCommand('&& b'))).toEqual(['', '&&'])
  })
})

// The two forms the scanner knowingly gets wrong. They are pinned rather than
// left undefined, so a future fix has a test to change deliberately instead of
// discovering the behaviour by accident. Both mis-segment; neither alters a
// byte, which is why they are acceptable.
describe('splitCommand known limitations', () => {
  it('splits inside a brace group, which is a display bug and not a data one', () => {
    const segs = splitCommand('{ a; b; }')
    expect(segs.length).toBeGreaterThan(1)
    expect(joinSegments(segs)).toBe('{ a; b; }')
  })

  it('splits inside a heredoc body, same trade', () => {
    const cmd = 'cat <<EOF\na | b\nEOF'
    const segs = splitCommand(cmd)
    expect(segs.length).toBeGreaterThan(1)
    expect(joinSegments(segs)).toBe(cmd)
  })
})
