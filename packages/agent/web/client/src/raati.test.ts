import { describe, it, expect } from 'vitest'
import { buildConveneArgs, raatiVerdictWord, raatiUnitCopyText, raatiResultCopyText, type ConveneForm } from './raati'
import type { ModelInfo, RaatiUnit, RaatiView } from './ctrlproto'

const models: ModelInfo[] = [
  { id: 'opus', provider: 'anthropic' },
  { id: 'gpt', provider: 'openai' },
]

// A blank convene form; each test sets only the fields it exercises.
const blank = (over: Partial<ConveneForm> = {}): ConveneForm => ({
  question: '',
  profile: '',
  cls: '',
  level: '',
  singleRound: false,
  inquire: '',
  converge: false,
  seatOrder: '',
  binding: '',
  ladderProv: '',
  seatPicks: [],
  evidence: '',
  conversation: '',
  ...over,
})

describe('buildConveneArgs — convene form serialization', () => {
  it('returns null for an empty (whitespace-only) question', () => {
    expect(buildConveneArgs(blank({ question: '   ' }), models)).toBeNull()
  })

  it('serializes a minimal question, trimmed', () => {
    expect(buildConveneArgs(blank({ question: '  ship it?  ' }), models)).toEqual({ question: 'ship it?' })
  })

  it('carries scalar options and the single_round / converge flags', () => {
    const args = buildConveneArgs(
      blank({
        question: 'q',
        profile: 'ethics',
        cls: 'veto',
        level: '1',
        singleRound: true,
        inquire: 'record',
        converge: true,
        seatOrder: 'turn',
        conversation: 'sess-1',
      }),
      models,
    )
    // level '1' with no ladder provider set, so no provider/model keys.
    expect(args).toEqual({
      question: 'q',
      profile: 'ethics',
      class: 'veto',
      level: '1',
      single_round: 'true',
      inquire: 'record',
      rounds: '3',
      seat_order: 'turn',
      conversation: 'sess-1',
    })
  })

  it('level 0 binding resolves the chosen model index to provider+model', () => {
    const args = buildConveneArgs(blank({ question: 'q', level: '0', binding: '1' }), models)
    expect(args).toMatchObject({ level: '0', provider: 'openai', model: 'gpt' })
  })

  it('level 1 uses the ladder provider', () => {
    const args = buildConveneArgs(blank({ question: 'q', level: '1', ladderProv: 'anthropic' }), models)
    expect(args).toMatchObject({ level: '1', provider: 'anthropic' })
  })

  it('level 2 serializes a full seat map to provider_i / model_i', () => {
    const args = buildConveneArgs(blank({ question: 'q', level: '2', seatPicks: ['0', '1', '0'] }), models)
    expect(args).toMatchObject({
      provider_0: 'anthropic',
      model_0: 'opus',
      provider_1: 'openai',
      model_1: 'gpt',
      provider_2: 'anthropic',
      model_2: 'opus',
    })
  })

  it('returns null for a partially filled level-2 seat map', () => {
    expect(buildConveneArgs(blank({ question: 'q', level: '2', seatPicks: ['0', '', ''] }), models)).toBeNull()
  })

  it('omits evidence when whitespace-only but keeps a real one', () => {
    expect(buildConveneArgs(blank({ question: 'q', evidence: '   ' }), models)).toEqual({ question: 'q' })
    expect(buildConveneArgs(blank({ question: 'q', evidence: 'logs' }), models)).toMatchObject({ evidence: 'logs' })
  })
})

describe('raatiVerdictWord', () => {
  it('maps known verdict tokens to display words', () => {
    expect(raatiVerdictWord('approve')).toBe('APPROVE')
    expect(raatiVerdictWord('rejected')).toBe('REJECTED')
    expect(raatiVerdictWord('escalated')).toBe('ESCALATED')
  })
  it('upper-cases an unknown token', () => {
    expect(raatiVerdictWord('weird')).toBe('WEIRD')
  })
})

describe('raatiUnitCopyText', () => {
  it('renders a voted ballot with binding, confidence, and rationale', () => {
    const u: RaatiUnit = {
      name: 'MELCHIOR-1',
      binding: 'anthropic/opus',
      status: 'voted',
      verdict: 'approve',
      confidence: 0.8,
      rationale: 'sound',
    }
    expect(raatiUnitCopyText(u)).toBe('MELCHIOR-1 (anthropic/opus): approve (0.80) — sound')
  })
  it('flags a blind ballot that differs from the open verdict', () => {
    const u: RaatiUnit = { name: 'BALTHASAR-2', status: 'voted', verdict: 'approve', blind: 'reject', rationale: 'changed mind' }
    expect(raatiUnitCopyText(u)).toContain('[blind ballot: reject]')
  })
  it('renders an absent seat with its reason', () => {
    const u: RaatiUnit = { name: 'CASPER-3', status: 'absent', why: 'timeout' }
    expect(raatiUnitCopyText(u)).toBe('CASPER-3: absent — timeout')
  })
})

describe('raatiResultCopyText', () => {
  it('serializes the question, verdict+tally, seats, and minority report', () => {
    const v: RaatiView = {
      running: false,
      question: 'ship?',
      decision: 'approved',
      tally: { approve: 2, reject: 1, abstain: 0, absent: 0 },
      units: [
        { name: 'MELCHIOR-1', status: 'voted', verdict: 'approve', rationale: 'yes' },
        { name: 'BALTHASAR-2', status: 'voted', verdict: 'reject', rationale: 'no' },
      ],
      minority: [{ unit: 'BALTHASAR-2', rationale: 'risk' }],
    }
    const out = raatiResultCopyText(v)
    expect(out).toContain('question: ship?')
    expect(out).toContain('verdict: APPROVED — 2 approve / 1 reject / 0 abstain / 0 absent')
    expect(out).toContain('MELCHIOR-1: approve — yes')
    expect(out).toContain('minority report:')
    expect(out).toContain('  BALTHASAR-2: risk')
  })
  it('marks a degraded verdict and omits the tally when absent', () => {
    const v: RaatiView = { running: false, decision: 'escalated', degraded: true }
    expect(raatiResultCopyText(v)).toBe('verdict: ESCALATED (degraded)')
  })
})
