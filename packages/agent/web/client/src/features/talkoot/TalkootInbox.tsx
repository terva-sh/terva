import { t, tn } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { Decision, TalkootCard, TalkootKickoff, TalkootProposal } from '../../platform/ctrlproto/types'
import { AskRequest } from '../interactions/AskRequest'
import { PermissionRequest } from '../interactions/PermissionRequest'
import { who } from './RoomLine'
import { MarkOf } from './MemberMark'

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
  const act = (p: Promise<unknown>) => p.then(() => onError(''), (e: unknown) => onError(e instanceof Error ? e.message : String(e)))
  return (
    <section class="talkoot-inbox" aria-label={t('Inbox')}>
      <h3 class="talkoot-inbox-head">{tn(cards.length, '%d card waits for you', '%d cards wait for you')}</h3>
      {cards.map((c) => (
        <div key={`${c.kind}:${c.session}:${c.id}`} class="talkoot-card">
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
            <AskRequest
              request={c.ask}
              onAnswer={(askID, answers) =>
                void act(client.send('answer', { ask_id: askID, answer: answers[0] ?? { answer: '' }, answers }, c.session))
              }
            />
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
      ))}
    </section>
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

// ProposalCard is the inbox form of a roster proposal: what it changes, and
// each field that widens a member's authority. The full member card, with
// look previews and edits, is TKT-01M39VWN0.
function ProposalCard({ proposal, onDecide }: { proposal: TalkootProposal; onDecide: (d: 'approve' | 'decline') => void }) {
  return (
    <div class={`card talkoot-proposal class-${proposal.class}`}>
      <div class="card-head">
        {proposal.title} <span class="talkoot-kind">{proposal.class}</span>
      </div>
      <p>{proposal.summary}</p>
      {proposal.why && <p class="talkoot-note">{proposal.why}</p>}
      <ul class="talkoot-changes">
        {proposal.changes.map((ch) => (
          <li key={ch.member}>
            <code>{ch.member}</code>{' '}
            {!ch.before ? t('joins') : !ch.after ? t('leaves') : t('changes')}
            {ch.widens && ch.widens.length > 0 && (
              <span class="talkoot-widens">{t('widens %s', ch.widens.join(', '))}</span>
            )}
          </li>
        ))}
      </ul>
      {proposal.persona && (
        <details>
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
