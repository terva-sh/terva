import { t, tn } from '../../i18n'
import type { TalkootAnswered, TalkootLine } from '../../platform/ctrlproto/types'
import { HUMAN_PREFIX, isPerson, isRoutine, lineKey } from '../../platform/talkoot/store'
import { clockTime } from '../formatting'
import { Markdown } from '../Markdown'
import { RefChip } from './RefChip'
import { MarkOf } from './MemberMark'
import { kindName, limitLabel, limitRows } from './routines'

// who names a sender or a recipient: a member id, or a person's name without
// its human: prefix.
export function who(addr: string): string {
  if (addr.startsWith(RECRUITER_PREFIX)) return t('the recruiter')
  if (addr.startsWith(MANAGER_PREFIX)) return t('the team manager')
  return isPerson(addr) ? addr.slice(HUMAN_PREFIX.length) : addr
}

// RECRUITER_PREFIX marks a recruiter session as a proposer
// (talkoot.RecruiterPrefix). The session id after it means nothing to a
// person.
const RECRUITER_PREFIX = 'recruiter:'

// MANAGER_PREFIX marks the team manager as a proposer (talkoot.ManagerPrefix).
const MANAGER_PREFIX = 'manager:'

// dayTime writes an instant as the time of day on this device, with the date
// in front when it is not today.
export function dayTime(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const two = (n: number) => String(n).padStart(2, '0')
  const time = `${two(d.getHours())}:${two(d.getMinutes())}`
  const now = new Date()
  const today = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate()
  return today ? time : `${d.getFullYear()}-${two(d.getMonth() + 1)}-${two(d.getDate())} ${time}`
}

// answerText is a person's answers, one per question.
function answerText(answers: TalkootAnswered[]): string {
  return answers.map((a) => a.chosen?.join(', ') || a.note || (a.declined ? t('declined') : '')).join('; ')
}

// RoomLine renders one room line. An envelope shows its sender, recipients,
// kind, body, and references. The other line types the view shows are one
// sentence each. A caller that may steer passes the chain actions.
export function RoomLine({
  line,
  onPauseChain,
  onResumeChain,
}: {
  line: TalkootLine
  onPauseChain?: (chain: string) => void
  onResumeChain?: (chain: string) => void
}) {
  const e = line.envelope
  if (e) {
    return (
      <article class={`talkoot-line envelope kind-${e.kind}${isPerson(e.from) ? ' from-person' : ''}`}>
        <header class="talkoot-line-head">
          {/* A routine has no mark of its own. Its run works as its member. */}
          <MarkOf id={isRoutine(e.from) ? (line.member ?? e.to[0]) : e.from} />
          <strong>{isRoutine(e.from) ? t('A routine of %s', line.member ?? e.to[0] ?? '') : who(e.from)}</strong>
          {/* A wake goes from a member to itself: terva sent it, at the time the member asked for. */}
          {e.kind === 'wake' ? (
            <span class="talkoot-kind">{t('woke itself')}</span>
          ) : e.kind === 'run' ? (
            // 🔑 A run is to nobody. terva started it at the routine's time,
            // and its body is the prompt that a person approved.
            <span class="talkoot-kind">{t('run, with no person present')}</span>
          ) : (
            <>
              <span class="talkoot-line-to">→ {e.to.map(who).join(', ')}</span>
              {e.kind !== 'message' && <span class="talkoot-kind">{e.kind}</span>}
            </>
          )}
          <time class="talkoot-time" dateTime={line.at}>
            {clockTime(line.at)}
          </time>
          {onPauseChain && (
            <button class="btn sm ghost" title={t('Pause the chain this message belongs to')} onClick={() => onPauseChain(e.chain.root)}>
              ⏸
            </button>
          )}
        </header>
        <Markdown class="talkoot-body" text={e.body} />
        {e.refs && e.refs.length > 0 && (
          <ul class="talkoot-refs">
            {e.refs.map((r) => (
              <li key={r}>
                <RefChip refText={r} />
              </li>
            ))}
          </ul>
        )}
        {e.cites?.map((c) => (
          <blockquote key={c.answer} class="talkoot-cite">
            {t('The answer to %s:', c.asker)} {answerText(c.answers)}
          </blockquote>
        ))}
      </article>
    )
  }
  switch (line.type) {
    case 'intro':
      return (
        <article class="talkoot-line intro">
          <header class="talkoot-line-head">
            <MarkOf id={line.member} />
            <strong>{line.member}</strong>
            <span class="talkoot-kind">{t('introduction')}</span>
          </header>
          <Markdown class="talkoot-body" text={line.text ?? ''} />
        </article>
      )
    case 'answer':
      return (
        <div class="talkoot-line system">
          {/* The daemon records no person on an answer line yet. */}
          {line.by ? t('%s answered %s', who(line.by), line.member ?? '') : t('The answer to %s', line.member ?? '')}:{' '}
          {answerText(line.answers ?? [])}
        </div>
      )
    case 'roster': {
      const swatch = line.color_after && (
        <span class="talkoot-swatch-dot" style={{ background: line.color_after }} aria-label={line.color_after} />
      )
      // The line does not record which form of update wrote it, and a text
      // update can change the colour with the members or the team's other
      // fields. So a colour change sits beside the members, and the line
      // says the roster changed rather than the colour alone.
      return (
        <div class="talkoot-line system">
          {line.proposal
            ? t('%s approved a roster change from %s', who(line.by ?? ''), who(line.proposer ?? ''))
            : t('%s changed the roster', who(line.by ?? ''))}
          {line.changes && line.changes.length > 0 && ` (${line.changes.map((c) => c.member).join(', ')})`}
          {line.team && line.team.length > 0 && `; ${t('the team: %s', line.team.map((c) => c.field).join(', '))}`}
          {line.reason && `; ${line.reason}`}
          {swatch && (
            <>
              {'; '}
              {t('the team colour')} {swatch}
            </>
          )}
        </div>
      )
    }
    case 'guard':
      return (
        <div class="talkoot-line system warn">
          {t('Guard %s: %s', line.guard ?? '', line.reason ?? line.action ?? '')}
          {onResumeChain && line.chain && (
            <button class="btn sm ghost" onClick={() => onResumeChain(line.chain!)}>
              {t('Resume the chain')}
            </button>
          )}
        </div>
      )
    case 'resume':
      return <div class="talkoot-line system">{t('%s resumed %s', who(line.by ?? ''), line.member || line.chain || t('the team'))}</div>
    case 'wake':
      switch (line.action) {
        case 'set':
          return (
            <div class="talkoot-line system">
              {t('%s set a wake for %s: %s', line.member ?? '', dayTime(line.due), line.text ?? '')}
            </div>
          )
        case 'cancelled':
          return <div class="talkoot-line system">{t('%s cancelled a wake of %s: %s', who(line.by ?? ''), line.member ?? '', line.text ?? '')}</div>
        case 'dropped':
          return (
            <div class="talkoot-line system warn">
              {t('A wake of %s did not fire (%s): %s', line.member ?? '', line.reason ?? '', line.text ?? '')}
            </div>
          )
      }
      return null
    case 'routine': {
      const by = who(line.by ?? '')
      const member = line.member ?? ''
      const why = line.reason ? `: ${line.reason}` : ''
      switch (line.action) {
        case 'approved': {
          const r = line.routine
          // 🔑 The card hid each limit that matched its default. The room
          // shows them all, so a person can read what the approval gave.
          return (
            <div class="talkoot-line system">
              {t('%s approved a %s routine of %s, %s', by, kindName(r?.kind ?? ''), member, r?.schedule ?? '')}
              {r && ` (${limitRows(r.fields).map((row) => `${limitLabel(row.field)}: ${row.value}`).join('; ')})`}
            </div>
          )
        }
        case 'paused':
          // A line with no person is terva's own pause, after a failed run.
          if (!line.by) return <div class="talkoot-line system warn">{t('A routine of %s is paused', member) + why}</div>
          return <div class="talkoot-line system">{t('%s paused a routine of %s', by, member) + why}</div>
        case 'skipped':
          return <div class="talkoot-line system">{t('A routine of %s started no run', member) + why}</div>
        case 'ended':
          return <div class="talkoot-line system">{t('A routine of %s ended', member) + why}</div>
        case 'resumed':
          return <div class="talkoot-line system">{t('%s resumed a routine of %s', by, member) + why}</div>
        case 'deleted':
          // The record of a routine that does not read names no member.
          return <div class="talkoot-line system">{(member ? t('%s deleted a routine of %s', by, member) : t('%s deleted a routine whose record did not read', by)) + why}</div>
      }
      return null
    }
    case 'damaged':
      return <div class="talkoot-line system warn">{t('A room line could not be read.')}</div>
  }
  return null
}

// RoomExchange is a run of messages between two members, collapsed to one
// line that expands in place. Teammate traffic is where a chain runs away, so
// the collapsed line keeps the pause for the newest message's chain. The
// parent may hold whether it is open, so an older page that extends the run,
// and so remounts it, leaves it as the person had it.
export function RoomExchange({
  members,
  lines,
  onPauseChain,
  open,
  onToggle,
}: {
  members: [string, string]
  lines: TalkootLine[]
  onPauseChain?: (chain: string) => void
  open?: boolean
  onToggle?: (open: boolean) => void
}) {
  const chain = lines[lines.length - 1]?.envelope?.chain.root
  return (
    <details class="talkoot-exchange" open={open} onToggle={onToggle && ((e) => onToggle((e.currentTarget as HTMLDetailsElement).open))}>
      <summary>
        {t('%s and %s', members[0], members[1])}, {tn(lines.length, '%d message', '%d messages')}
        {onPauseChain && chain && (
          <button
            class="btn sm ghost"
            title={t('Pause the chain this message belongs to')}
            onClick={(e) => {
              e.preventDefault()
              onPauseChain(chain)
            }}
          >
            ⏸
          </button>
        )}
      </summary>
      {lines.map((l) => (
        <RoomLine key={lineKey(l)} line={l} onPauseChain={onPauseChain} />
      ))}
    </details>
  )
}
