import type { TalkootMark, TalkootPreview, TalkootRosterMember } from '../../platform/ctrlproto/types'

// A member as the team builder holds it. The defaults a preview fills in are
// optional here, so an empty field stays out of the roster.
export type DraftMember = Omit<TalkootRosterMember, 'driver' | 'posture' | 'workspace'> & {
  driver?: string
  posture?: string
  workspace?: string
}

export interface DraftTeam {
  name: string
  title: string
  budget: string
  members: DraftMember[]
  charter: string
}

// The member keys in the order a roster lists them (talkoot.MemberFields).
const FIELDS: (keyof DraftMember)[] = [
  'id', 'role', 'title', 'mark', 'persona', 'driver', 'model', 'tier', 'posture', 'workspace',
  'reviewer', 'budget_usd_per_day', 'turns_per_day', 'tools', 'idle_stop',
]

// scalar writes one YAML value. A string goes as a JSON string, which YAML
// reads as a double-quoted scalar, so no value can open a new key.
function scalar(v: unknown): string {
  if (typeof v === 'boolean') return String(v)
  // ⚠️ A number that does not parse goes as its text, so the daemon refuses
  // it. Writing null would remove the cap in silence.
  if (typeof v === 'number') return Number.isFinite(v) ? String(v) : JSON.stringify(String(v))
  if (Array.isArray(v)) return `[${v.map((x) => JSON.stringify(String(x))).join(', ')}]`
  if (v && typeof v === 'object') {
    const m = v as TalkootMark
    const parts = [m.shape ? `shape: ${JSON.stringify(m.shape)}` : '', m.color ? `color: ${JSON.stringify(m.color)}` : ''].filter(Boolean)
    return `{${parts.join(', ')}}`
  }
  return JSON.stringify(String(v).trim())
}

function empty(v: unknown): boolean {
  if (v === undefined || v === null || v === false || v === 0) return true
  if (typeof v === 'string') return v.trim() === ''
  if (Array.isArray(v)) return v.length === 0
  if (typeof v === 'object') return !(v as TalkootMark).shape && !(v as TalkootMark).color
  return false
}

// composeRoster writes the talkoot.md a draft becomes, without a home. The
// daemon fills in home from its own directory when it previews the text.
export function composeRoster(team: DraftTeam): string {
  const budget = team.budget.trim() === '' ? NaN : Number(team.budget)
  const out = ['---', `name: ${scalar(team.name)}`]
  if (team.title.trim()) out.push(`title: ${scalar(team.title.trim())}`)
  out.push(`budget_usd_per_day: ${Number.isFinite(budget) ? String(budget) : JSON.stringify(team.budget)}`)
  out.push('members:')
  for (const m of team.members) {
    let first = true
    for (const f of FIELDS) {
      const v = m[f]
      if (f !== 'id' && empty(v)) continue
      out.push(`${first ? '  - ' : '    '}${f}: ${scalar(v ?? '')}`)
      first = false
    }
  }
  out.push('---')
  const charter = team.charter.trim()
  if (charter) out.push('', charter)
  return out.join('\n') + '\n'
}

// charterOf returns the body below a roster's frontmatter.
export function charterOf(text: string): string {
  const s = text.replace(/\r\n/g, '\n')
  if (!s.startsWith('---\n')) return ''
  const end = s.indexOf('\n---', 4)
  if (end < 0) return ''
  const after = s.slice(end + 4)
  const nl = after.indexOf('\n')
  return nl < 0 ? '' : after.slice(nl + 1).trim()
}

// draftFromPreview starts a draft from a template's preview: its members with
// the defaults applied, its budget, and its charter.
export function draftFromPreview(pv: TalkootPreview, name: string): DraftTeam {
  return {
    name,
    title: pv.template.title ?? '',
    budget: String(pv.budget_usd_per_day),
    members: pv.members.map(({ available: _a, problem: _p, ...m }) => ({ ...m })),
    charter: charterOf(pv.text),
  }
}

// emptyDraft is a team of one coordinator, the smallest valid roster.
export function emptyDraft(name: string): DraftTeam {
  return { name, title: '', budget: '5', members: [{ id: 'lead', role: 'coordinator', persona: 'mieli' }], charter: '' }
}
