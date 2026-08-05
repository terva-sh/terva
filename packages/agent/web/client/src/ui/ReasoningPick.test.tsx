// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/preact'
import type { ReasoningRungInfo } from '../platform/ctrlproto/types'
import { ReasoningPick, rungDetail } from './ReasoningPick'

afterEach(cleanup)

// These rows are the shape models.list hands over — see ctrlproto.ReasoningRungInfo
// and the Go guards in packages/agent/workspace/workspace_ladder_test.go, which
// pin that the daemon really sends this for these models. What is tested HERE is
// the other half: that the picker says what the rows mean rather than inventing
// a number.

// Codex/Responses: an effort enum, no budget anywhere.
const codexRungs: ReasoningRungInfo[] = [
  { level: 'off' },
  { level: 'minimum', effort: 'low', same_as: 'low' },
  { level: 'low', effort: 'low' },
  { level: 'medium', effort: 'medium' },
  { level: 'high', effort: 'high' },
  { level: 'maximum', effort: 'xhigh' },
  { level: 'max', effort: 'max' },
]

// Gemini 3: four rungs land on one enum.
const geminiRungs: ReasoningRungInfo[] = [
  { level: 'off' },
  { level: 'minimum', effort: 'LOW', same_as: 'low' },
  { level: 'low', effort: 'LOW' },
  { level: 'medium', effort: 'HIGH', same_as: 'high' },
  { level: 'high', effort: 'HIGH' },
  { level: 'maximum', effort: 'HIGH', same_as: 'high' },
  { level: 'max', effort: 'HIGH', same_as: 'high' },
]

// Anthropic: a real budget, already clamped by the model's output cap — note
// maximum is 24576, not the ladder's 32768.
const claudeRungs: ReasoningRungInfo[] = [
  { level: 'off' },
  { level: 'minimum', budget: 1024 },
  { level: 'low', budget: 2048 },
  { level: 'medium', budget: 8192 },
  { level: 'high', budget: 16384 },
  { level: 'maximum', budget: 24576 },
  { level: 'max', budget: 24576, same_as: 'maximum' },
]

describe('rungDetail', () => {
  // THE BUG THIS REPLACES: the picker held a hardcoded budget table and printed
  // "~8k tokens of thinking" for every model at medium — including models whose
  // request carries no budget field at all.
  it('never describes an effort-wire model in tokens', () => {
    for (const rows of [codexRungs, geminiRungs]) {
      for (const r of rows) {
        expect(rungDetail(r.level, r, false)).not.toContain('tokens of thinking')
      }
    }
  })

  // The other half, so the fix above cannot be "delete the budget wording": a
  // model that genuinely takes a budget must still show one, and it must be the
  // CLAMPED value rather than the ladder constant.
  it('shows a budget-wire model its clamped budget', () => {
    const medium = claudeRungs.find((r) => r.level === 'medium')!
    expect(rungDetail('medium', medium, false)).toContain('8k tokens of thinking')
    const maximum = claudeRungs.find((r) => r.level === 'maximum')!
    expect(rungDetail('maximum', maximum, false)).toContain('24k')
    expect(rungDetail('maximum', maximum, false)).not.toContain('32k')
  })

  it('names the canonical rung a collapsed one lands on', () => {
    const minimum = geminiRungs.find((r) => r.level === 'minimum')!
    expect(rungDetail('minimum', minimum, false)).toContain('same as low')
    // ...and the canonical rung carries no annotation, or it would claim to be
    // the same as itself.
    const low = geminiRungs.find((r) => r.level === 'low')!
    expect(rungDetail('low', low, false)).not.toContain('same as')
  })

  // Native and duplicate are mutually exclusive claims about the top rung.
  it('calls a native max native, and never also a duplicate', () => {
    const max = codexRungs.find((r) => r.level === 'max')!
    const detail = rungDetail('max', max, true)
    expect(detail).toContain('native on this model')
    expect(detail).not.toContain('same as')
  })

  it('distinguishes a model with no thinking setting from one switched off', () => {
    expect(rungDetail('high', undefined, false)).toBe('this model takes no thinking setting')
    expect(rungDetail('off', { level: 'off' }, false)).toBe('no thinking')
  })
})

describe('ReasoningPick', () => {
  it('renders the daemon rows rather than a ladder-wide budget', () => {
    render(
      <ReasoningPick
        override=""
        global=""
        rungs={geminiRungs}
        maxIsNative={false}
        onPick={() => {}}
        onClose={() => {}}
      />,
    )
    // Every rung is still offered — the ladder keeps one shape across models —
    // but the ones that collapse say so.
    expect(screen.getByText('effort: LOW')).toBeTruthy()
    // medium, maximum and max all land on HIGH — three rows saying so is the
    // point, not a duplicate-match accident.
    expect(screen.getAllByText('effort: HIGH — same as high on this model').length).toBe(3)
    expect(screen.queryByText(/tokens of thinking/)).toBeNull()
  })

  it('says a model takes no thinking setting when it carries no ladder', () => {
    render(
      <ReasoningPick override="" global="" onPick={() => {}} onClose={() => {}} />,
    )
    expect(screen.getAllByText('this model takes no thinking setting').length).toBe(7)
  })
})
