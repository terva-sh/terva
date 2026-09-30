import { t, tn } from '../../i18n'
import type { TalkootAnswered, TalkootLine } from '../../platform/ctrlproto/types'
import { HUMAN_PREFIX, isPerson, lineKey } from '../../platform/talkoot/store'
import { clockTime } from '../../ui/formatting'
import { Markdown } from '../../ui/Markdown'
import { RefChip } from './RefChip'

// who names a sender or a recipient: a member id, or a person's name without
// its human: prefix.
export function who(addr: string): string {
  return isPerson(addr) ? addr.slice(HUMAN_PREFIX.length) : addr
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
          <strong>{who(e.from)}</strong>
          <span class="talkoot-line-to">→ {e.to.map(who).join(', ')}</span>
          {e.kind !== 'message' && <span class="talkoot-kind">{e.kind}</span>}
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
    case 'roster':
      return (
        <div class="talkoot-line system">
          {line.proposal
            ? t('%s approved a roster change from %s', who(line.by ?? ''), who(line.proposer ?? ''))
            : t('%s changed the roster', who(line.by ?? ''))}
          {line.changes && line.changes.length > 0 && ` (${line.changes.map((c) => c.member).join(', ')})`}
        </div>
      )
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
