// Pure Raati (deliberation board) logic, split out of app.tsx so it can be
// unit-tested without a DOM: the convene-form serializer and the clipboard /
// verdict text renderers. The components in app.tsx import from here.
import type { ModelInfo, RaatiUnit, RaatiView } from './ctrlproto'
import { t } from './i18n'

// ConveneForm is the raati convene form's state, as the composer holds it.
export interface ConveneForm {
  question: string
  profile: string
  cls: string
  level: string
  singleRound: boolean
  inquire: string
  converge: boolean
  seatOrder: string
  binding: string // index into models for a level-0 single-model binding
  ladderProv: string // level-1 provider
  seatPicks: string[] // per-seat indices into models for level 2 ('' = unset)
  evidence: string
  conversation: string
}

// buildConveneArgs serializes the convene form into the raati.convene action
// args, or null when the form is not submittable: an empty question, or a
// level-2 seat map that is only partially filled (some seats chosen, some not).
// Pure — no component state, no DOM.
export function buildConveneArgs(f: ConveneForm, models: ModelInfo[] | undefined): Record<string, string> | null {
  const q = f.question.trim()
  const seatsChosen = f.seatPicks.filter((s) => s !== '').length
  const seatsPartial = f.level === '2' && seatsChosen > 0 && seatsChosen < f.seatPicks.length
  if (!q || seatsPartial) return null
  const args: Record<string, string> = { question: q }
  if (f.profile) args.profile = f.profile
  if (f.cls) args.class = f.cls
  if (f.level) args.level = f.level
  if (f.singleRound) args.single_round = 'true'
  if (f.inquire) args.inquire = f.inquire
  if (f.converge) args.rounds = '3'
  if (f.seatOrder) args.seat_order = f.seatOrder
  if (f.level === '0' && f.binding !== '') {
    const m = (models ?? [])[Number(f.binding)]
    if (m) {
      args.provider = m.provider
      args.model = m.id
    }
  }
  if (f.level === '1' && f.ladderProv) args.provider = f.ladderProv
  if (f.level === '2' && seatsChosen === f.seatPicks.length) {
    f.seatPicks.forEach((s, i) => {
      const m = (models ?? [])[Number(s)]
      if (m) {
        args[`provider_${i}`] = m.provider
        args[`model_${i}`] = m.id
      }
    })
  }
  if (f.evidence.trim()) args.evidence = f.evidence
  if (f.conversation) args.conversation = f.conversation
  return args
}

// raatiVerdictWord maps a verdict token to its display word.
export function raatiVerdictWord(v: string): string {
  switch (v) {
    case 'approve':
      return t('APPROVE')
    case 'reject':
      return t('REJECT')
    case 'abstain':
      return t('ABSTAIN')
    case 'approved':
      return t('APPROVED')
    case 'rejected':
      return t('REJECTED')
    case 'escalated':
      return t('ESCALATED')
  }
  return v.toUpperCase()
}

// raatiUnitCopyText renders one panelist's position for the clipboard.
export function raatiUnitCopyText(u: RaatiUnit): string {
  const who = u.binding ? `${u.name} (${u.binding})` : u.name
  if (u.status === 'absent') return `${who}: absent — ${u.why ?? ''}`
  const conf = typeof u.confidence === 'number' ? ` (${u.confidence.toFixed(2)})` : ''
  const blind = u.blind && u.blind !== u.verdict ? ` [blind ballot: ${u.blind}]` : ''
  return `${who}: ${u.verdict ?? ''}${conf}${blind} — ${u.rationale ?? ''}`
}

// raatiResultCopyText renders the whole outcome — verdict, tally, every seat's
// position, and the minority report — for the clipboard.
export function raatiResultCopyText(v: RaatiView): string {
  const lines: string[] = []
  if (v.question) lines.push(`question: ${v.question}`)
  const tally = v.tally
  lines.push(
    `verdict: ${(v.decision ?? '').toUpperCase()}${v.degraded ? ' (degraded)' : ''}` +
      (tally ? ` — ${tally.approve} approve / ${tally.reject} reject / ${tally.abstain} abstain / ${tally.absent} absent` : ''),
  )
  for (const u of v.units ?? []) lines.push(raatiUnitCopyText(u))
  if ((v.minority?.length ?? 0) > 0) {
    lines.push('minority report:')
    for (const m of v.minority ?? []) lines.push(`  ${m.unit}: ${m.rationale ?? ''}`)
  }
  return lines.join('\n')
}
