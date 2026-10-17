import { t, tn } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import { useContext } from 'preact/hooks'
import type { Decision, TalkootCard, TalkootKickoff, TalkootMemberChange, TalkootMemberEntry, TalkootProposal, TalkootTeamChange } from '../../platform/ctrlproto/types'
import { AskRequest } from '../interactions/AskRequest'
import { PermissionRequest } from '../interactions/PermissionRequest'
import { who } from './RoomLine'
import { MarkOf, MarksContext, MemberMark } from './MemberMark'

// TalkootInbox shows every card that waits for a person: a member's question
// or approval, a roster proposal, and a new team's kickoff.
//
// A question or an approval is answered on the member's own session, which
// resolves the member's card and this one together. A proposal is decided
// with talkoot.decide, and a kickoff runs or is skipped with talkoot.kickoff.
// The daemon's talkoot_inbox_resolved event removes a card, so nothing here
// removes one by hand.
export function TalkootInbox({
  client,
  id,
  cards,
  person,
  canSteer,
  onError,
}: {
  client: ClientLike
  id: string
  cards: TalkootCard[]
  person: string
  canSteer: boolean
  onError: (msg: string) => void
}) {
  if (cards.length === 0) return null
  return (
    <section class="talkoot-inbox" aria-label={t('Inbox')}>
      <h3 class="talkoot-inbox-head">{tn(cards.length, '%d card waits for you', '%d cards wait for you')}</h3>
      {cards.map((c) => (
        <InboxCard key={`${c.kind}:${c.session}:${c.id}`} client={client} id={id} card={c} person={person} canSteer={canSteer} onError={onError} />
      ))}
    </section>
  )
}

// InboxCard is one card that waits for a person, with the controls that
// decide it. The panel lists every card in its inbox. The Talkoot app puts
// each card into the conversation at the time it opened.
export function InboxCard({
  client,
  id,
  card: c,
  person,
  canSteer,
  onError,
}: {
  client: ClientLike
  id: string
  card: TalkootCard
  person: string
  canSteer: boolean
  onError: (msg: string) => void
}) {
  const act = (p: Promise<unknown>) => p.then(() => onError(''), (e: unknown) => onError(e instanceof Error ? e.message : String(e)))
  return (
    <div class="talkoot-card">
      {c.member && (
        <div class="talkoot-card-from">
          <MarkOf id={c.member} /> {who(c.member)}
        </div>
      )}
      {!canSteer ? (
        <div class="talkoot-note">{cardSummary(c)}</div>
      ) : c.kind === 'permission' && c.permission ? (
        <PermissionRequest
          request={c.permission}
          onDecide={(callID: string, decision: Decision) =>
            void act(client.send('approve', { call_id: callID, decision }, c.session))
          }
        />
      ) : c.kind === 'ask' && c.ask ? (
        <>
          {/* The room keeps the answer sealed, so it cannot be redacted
              later. The card is the one place to warn. */}
          <div class="talkoot-note talkoot-ask-kept">{t('This answer is kept in the talkoot room, and a teammate may see it.')}</div>
          <AskRequest
            request={c.ask}
            onAnswer={(askID, answers) =>
              void act(client.send('answer', { ask_id: askID, answer: answers[0] ?? { answer: '' }, answers }, c.session))
            }
          />
        </>
      ) : c.kind === 'proposal' && c.proposal ? (
        <ProposalCard
          proposal={c.proposal}
          onDecide={(decision) =>
            void act(client.send('talkoot.decide', { id, by: person, proposal: c.proposal!.id, decision }))
          }
        />
      ) : c.kind === 'kickoff' && c.kickoff ? (
        <KickoffCard
          kickoff={c.kickoff}
          onRun={(skip) => void act(client.send('talkoot.kickoff', { id, by: person, skip }))}
        />
      ) : (
        <div class="talkoot-note">{cardSummary(c)}</div>
      )}
    </div>
  )
}

function cardSummary(c: TalkootCard): string {
  switch (c.kind) {
    case 'permission':
      return t('%s asks to run %s', who(c.member), c.permission?.tool ?? '')
    case 'ask':
      return t('%s has a question', who(c.member))
    case 'proposal':
      return t('A roster proposal: %s', c.proposal?.title ?? '')
    case 'kickoff':
      return t('The team waits for its kickoff')
  }
}

// ProposalCard is the inbox form of a roster proposal: each member it
// touches, field by field, with every authority field marked and each one
// that grants more flagged. A look batch, such as a theme, shows the marks
// before and after side by side. A new persona shows in full.
function ProposalCard({ proposal, onDecide }: { proposal: TalkootProposal; onDecide: (d: 'approve' | 'decline') => void }) {
  const looks = proposal.changes.filter(
    (ch) => ch.before && ch.after && (markOf(ch.before) !== markOf(ch.after) || fieldText(ch.mark_before) !== fieldText(ch.mark_after)),
  )
  return (
    <div class={`card talkoot-proposal class-${proposal.class}`}>
      <div class="card-head">
        {proposal.title} <span class="talkoot-kind">{proposal.class}</span>
      </div>
      <p>{proposal.summary}</p>
      {proposal.why && <p class="talkoot-note">{proposal.why}</p>}
      {proposal.team && proposal.team.length > 0 && <TeamFields changes={proposal.team} />}
      {looks.length > 0 && <LookBatch changes={looks} />}
      <ul class="talkoot-changes">
        {proposal.changes.map((ch) => (
          <li key={ch.member}>
            <code>{ch.member}</code>{' '}
            {!ch.before ? t('joins') : !ch.after ? t('leaves') : t('changes')}
            {ch.widens && ch.widens.length > 0 && (
              <span class="talkoot-widens">{t('widens %s', ch.widens.join(', '))}</span>
            )}
            <ChangeFields change={ch} />
          </li>
        ))}
      </ul>
      {proposal.persona && (
        <details open>
          <summary>{t('New persona: %s', proposal.persona.name)}</summary>
          <pre class="preview">{proposal.persona.text}</pre>
        </details>
      )}
      {proposal.problem && <div class="talkoot-error">{proposal.problem}</div>}
      <div class="card-actions">
        <button class="btn primary" onClick={() => onDecide('approve')}>
          {t('Approve')}
        </button>
        <button class="btn" onClick={() => onDecide('decline')}>
          {t('Decline')}
        </button>
      </div>
    </div>
  )
}

// The fields of a member entry, in the order the roster writes them.
const ENTRY_FIELDS: (keyof TalkootMemberEntry)[] = [
  'role', 'title', 'mark', 'persona', 'driver', 'model', 'tier', 'posture',
  'workspace', 'reviewer', 'budget_usd_per_day', 'turns_per_day', 'tools', 'idle_stop',
]

function fieldText(v: unknown): string {
  if (v === undefined || v === null || v === '') return ''
  if (Array.isArray(v)) return v.join(', ')
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

function markOf(e: TalkootMemberEntry): string {
  return fieldText(e.mark)
}

// ChangeFields lists a member's fields: every field of a member that joins or
// leaves, and the fields that change otherwise, before and after.
function ChangeFields({ change }: { change: TalkootMemberChange }) {
  const { before, after } = change
  const rows = ENTRY_FIELDS.filter((f) => {
    const b = before ? fieldText(before[f]) : ''
    const a = after ? fieldText(after[f]) : ''
    return f !== 'id' && (before && after ? b !== a : (a || b) !== '')
  })
  if (rows.length === 0) return null
  return (
    <table class="talkoot-fields">
      <tbody>
        {rows.map((f) => {
          const authority = change.authority?.includes(f)
          const widens = change.widens?.includes(f)
          return (
            <tr key={f} class={`${authority ? 'authority' : ''}${widens ? ' widens' : ''}`}>
              <th>
                {f}
                {authority && <span class="talkoot-kind">{t('authority')}</span>}
              </th>
              {before && after && <td>{fieldText(before[f]) || t('(default)')}</td>}
              {before && after && <td>→</td>}
              <td>
                {fieldText((after ?? before)![f]) || t('(default)')}
                {widens && ` ${t('(grants more)')}`}
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

// TeamFields lists each team field a proposal changes, before and after, with
// its class. A raised budget is flagged. A charter is long, so it shows whole
// in a disclosure, before and after.
function TeamFields({ changes }: { changes: TalkootTeamChange[] }) {
  const short = changes.filter((c) => c.field !== 'charter')
  const charter = changes.find((c) => c.field === 'charter')
  return (
    <div class="talkoot-team-change">
      <div class="talkoot-note">{t('The team changes')}</div>
      {short.length > 0 && (
        <table class="talkoot-fields">
          <tbody>
            {short.map((c) => (
              <tr key={c.field} class={`${c.class === 'authority' ? 'authority' : ''}${c.widens ? ' widens' : ''}`}>
                <th>
                  {c.field}
                  {c.class === 'authority' && <span class="talkoot-kind">{t('authority')}</span>}
                </th>
                <td>{fieldText(c.before) || t('(default)')}</td>
                <td>→</td>
                <td>
                  {fieldText(c.after) || t('(default)')}
                  {c.widens && ` ${t('(grants more)')}`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {charter && (
        <details open>
          <summary>
            {t('The charter changes')} <span class="talkoot-kind">{charter.class}</span>
          </summary>
          <div class="talkoot-note">{t('Before')}</div>
          <pre class="preview">{fieldText(charter.before) || t('(none)')}</pre>
          <div class="talkoot-note">{t('After')}</div>
          <pre class="preview">{fieldText(charter.after) || t('(none)')}</pre>
        </details>
      )}
    </div>
  )
}

// LookBatch shows each restyled member's mark before and after, side by side.
// It draws the marks the daemon resolved for each roster, so a reset shows
// the default it returns to. A daemon from before them sends the entries
// alone, and an unset mark then draws as the member's mark today.
function LookBatch({ changes }: { changes: TalkootMemberChange[] }) {
  const marks = useContext(MarksContext)
  return (
    <div class="talkoot-look-batch" role="table" aria-label={t('Marks before and after')}>
      {changes.map((ch) => (
        <div key={ch.member} role="row" style={{ display: 'contents' }}>
          <code role="cell">{ch.member}</code>
          <span role="cell" data-side="before">
            <MemberMark mark={ch.mark_before ?? ch.before?.mark ?? marks[ch.member]} size={24} label={t('%s before', ch.member)} />
          </span>
          <span role="cell" data-side="after">
            <MemberMark mark={ch.mark_after ?? ch.after?.mark ?? marks[ch.member]} size={24} label={t('%s after', ch.member)} />
          </span>
        </div>
      ))}
    </div>
  )
}

// KickoffCard asks a person to start a new team's introductions, with the
// estimated cost, or to skip them.
function KickoffCard({ kickoff, onRun }: { kickoff: TalkootKickoff; onRun: (skip: boolean) => void }) {
  return (
    <div class="card talkoot-kickoff">
      <div class="card-head">{t('Start the team')}</div>
      <p>
        {t('Each member introduces itself, then the coordinator asks you the first question. Estimated cost: $%s.', kickoff.estimate_usd.toFixed(2))}
        {kickoff.unpriced_turns ? ` ${tn(kickoff.unpriced_turns, '%d turn has no price.', '%d turns have no price.')}` : ''}
      </p>
      <div class="card-actions">
        <button class="btn primary" onClick={() => onRun(false)}>
          {t('Run the introductions')}
        </button>
        <button class="btn" onClick={() => onRun(true)}>
          {t('Skip them')}
        </button>
      </div>
    </div>
  )
}
