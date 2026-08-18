import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import { downloadExport } from '../../ui/browser'
import { Hint } from './Hint'
import { ModelPick } from './ModelPick'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { CardSummary, CardView, CardExport, CardLintFinding, CardLintResult, DefaultForResult, Group } from '../../platform/ctrlproto/types'

// The normalized CCv2 `data` object inside CardView.raw (card.Marshal output is
// { spec, spec_version, data }). Only the fields the sheet renders are typed;
// unknown extensions round-trip untouched on the server.
interface CardData {
  description?: string
  personality?: string
  scenario?: string
  first_mes?: string
  mes_example?: string
  creator_notes?: string
  system_prompt?: string
  post_history_instructions?: string
  alternate_greetings?: string[]
  character_book?: { name?: string; entries?: BookEntry[] }
}
interface BookEntry {
  name?: string
  comment?: string
  keys?: string[]
  constant?: boolean
}

function greetingText(data: CardData, idx: number): string {
  if (idx <= 0) return data.first_mes ?? ''
  return data.alternate_greetings?.[idx - 1] ?? ''
}

// A labeled field block; renders "—" (muted) when empty, an optional ⓘ hint
// explaining what the field does (discoverability), and preserves the card
// author's line breaks.
function Field(props: { label: string; value?: string; hint?: string }) {
  return (
    <div class="stage-cardfield">
      <div class="stage-cardfield__label">
        {props.label}
        {props.hint && <Hint text={props.hint} />}
      </div>
      <p class={`stage-cardfield__value ${props.value?.trim() ? '' : 'stage-cardfield__value--empty'}`}>{props.value?.trim() || '—'}</p>
    </div>
  )
}

// CardSheet is the character detail sheet (rough-edge #8): tap a card's ⋯ to
// inspect the WHOLE card — every CCv2 field, not just the greeting — with the
// quick-start controls (greeting picker + Start) kept at the top so starting a
// chat stays one tap. Fetches the full card (cards.get) on open and renders from
// its raw CCv2 `data`; the ⓘ hints teach what each field drives (#10). The
// deterministic lint findings render here too (S2b).
export function CardSheet(props: {
  client: ClientLike
  card: CardSummary
  busy: boolean
  onClose: () => void
  onStart: (greeting: number) => void
  onEdit?: () => void
  // Copy this card under a new name, portrait included — the way to iterate on a
  // design without overwriting the version that works. Sits with the other
  // whole-card actions rather than beside ✎ Edit, because it makes a SECOND card
  // rather than changing this one.
  onDuplicate?: () => void
  // Delete lives here rather than on the grid tile: the tile's one corner slot is
  // already the "⋯ Details" button, and a destructive action reached by mistake
  // from a grid is worse than one reached deliberately from the detail sheet.
  onDelete?: () => void
  // Card groups: every group (present only when the daemon serves them), and a
  // toggle that files or unfiles THIS card. Distinct from the read-only CCv2 tags
  // above — groups are terva's own organization, added and removed right here.
  groups?: Group[]
  onToggleGroup?: (groupId: string) => void
}) {
  const { client, card, busy, onClose, onStart, onEdit, onDuplicate, onDelete, groups, onToggleGroup } = props
  const [view, setView] = useState<CardView | null>(null)
  const [findings, setFindings] = useState<CardLintFinding[] | null>(null)
  const [greeting, setGreeting] = useState(0)
  const [error, setError] = useState('')
  // This card's effective default model (Card → World → Workspace), resolved by
  // the daemon's one authority. `source === 'card'` means the card carries its
  // own default, shown as an active pick; anything else means it inherits, and
  // the picker's Default row names what from. Null until loaded; the whole block
  // hides on an old daemon that doesn't serve models.default_for.
  const [cardDefault, setCardDefault] = useState<DefaultForResult | null>(null)
  const [cardModelSupported, setCardModelSupported] = useState(true)

  useEffect(() => {
    client
      .send<CardView>('cards.get', { id: card.id })
      .then(setView)
      .catch((e: unknown) => setError(errText(e)))
    // The deterministic lint (cards.lint) rides its own fetch — a card that
    // fails to lint (an old daemon without the verb) still shows its details.
    client
      .send<CardLintResult>('cards.lint', { id: card.id })
      .then((r) => setFindings(r.findings ?? []))
      .catch(() => setFindings(null))
    // The card's default model rides its own fetch too — an unsupported verb just
    // hides the row, it never blocks the sheet.
    client
      .send<DefaultForResult>('models.default_for', { card: card.id })
      .then(setCardDefault)
      .catch(() => setCardModelSupported(false))
  }, [card.id])

  // Set or clear this card's default model. Empty strings clear it (fall back to
  // the workspace default); the picker's Default row fires exactly that. Refetch
  // so the picker reflects what the daemon now resolves.
  const setCardModel = (provider: string, model: string) => {
    setError('')
    client
      .send('cardmodel.set', { card: card.id, provider, model })
      .then(() => client.send<DefaultForResult>('models.default_for', { card: card.id }).then(setCardDefault))
      .catch((e: unknown) => setError(errText(e)))
  }

  const data = (view?.raw as { data?: CardData } | undefined)?.data
  const greetings = card.greetings || 1
  const book = data?.character_book?.entries ?? []
  const warns = findings?.filter((f) => f.severity === 'warn').length ?? 0
  const infos = (findings?.length ?? 0) - warns

  const exportCard = async () => {
    setError('')
    try {
      downloadExport(await client.send<CardExport>('cards.export', { id: card.id }))
    } catch (e) {
      setError(errText(e))
    }
  }

  return (
    <div class="stage-sheet-backdrop" onClick={onClose}>
      <div class="stage-sheet stage-sheet--detail" onClick={(e) => e.stopPropagation()}>
        <header class="stage-cardsheet__head">
          {card.avatar_url ? (
            <img class="stage-cardsheet__avatar" src={card.avatar_url} alt="" />
          ) : (
            <div class="stage-cardsheet__avatar stage-cardsheet__avatar--blank" aria-hidden="true">
              {(card.name[0] ?? '·').toUpperCase()}
            </div>
          )}
          <div class="stage-cardsheet__id">
            <h3>{card.name}</h3>
            <div class="stage-cardsheet__meta">
              {card.creator && <span>{card.creator}</span>}
              {card.character_version && <span>v{card.character_version}</span>}
              <span>{card.spec_version ? t('spec %s', card.spec_version) : t('v1 card')}</span>
            </div>
          </div>
          {onEdit && (
            <button class="stage-cardsheet__edit" title={t('Edit this card')} onClick={onEdit}>
              {t('✎ Edit')}
            </button>
          )}
          <button class="stage-drawer__close" onClick={onClose}>
            ✕
          </button>
        </header>

        {error && (
          <p class="stage-error" onClick={() => setError('')}>
            {error}
          </p>
        )}

        {(card.tags?.length ?? 0) > 0 && (
          <div class="stage-cardsheet__tags">
            {card.tags!.map((tg) => (
              <span key={tg} class="stage-cardsheet__tag">
                {tg}
              </span>
            ))}
          </div>
        )}

        {groups && onToggleGroup && (
          <div class="stage-cardsheet__groups">
            <span class="stage-cardsheet__groups-label">{t('Groups')}</span>
            {groups.length === 0 ? (
              <span class="stage-cardsheet__groups-empty">{t('No groups yet — make one from the library.')}</span>
            ) : (
              <div class="stage-cardsheet__grouprow">
                {groups.map((g) => {
                  const inIt = g.members.includes(card.id)
                  return (
                    <button
                      key={g.id}
                      class={`stage-cardsheet__grouptoggle ${inIt ? 'stage-cardsheet__grouptoggle--on' : ''}`}
                      aria-pressed={inIt}
                      onClick={() => onToggleGroup(g.id)}
                    >
                      {g.color && <span class="stage-cardgroup__dot" style={{ background: g.color }} aria-hidden="true" />}
                      {inIt ? '✓ ' : '+ '}
                      {g.name}
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        )}

        {/* This card's default model: terva-owned metadata like the groups above,
            NOT stored on the card. A chat started from the card (and its doctor)
            defaults to this; the Default row clears back to the workspace model. */}
        {cardModelSupported && (
          <div class="stage-cardsheet__defmodel">
            <span class="stage-cardsheet__defmodel-label">
              {t('Default model')}
              <Hint text={t("The provider + model a chat started from this card opens on, before the workspace default. Stored as terva metadata — never written into the card.")} />
            </span>
            <ModelPick
              client={client}
              sessionId=""
              onSelect={setCardModel}
              currentProvider={cardDefault?.source === 'card' ? cardDefault.provider : undefined}
              currentModel={cardDefault?.source === 'card' ? cardDefault.model : undefined}
              defaultLabel={t('Workspace default')}
            />
          </div>
        )}

        <div class="stage-cardsheet__facts">
          <span>
            {tn(greetings, '%d greeting', '%d greetings')}
          </span>
          {card.book_entries ? (
            <span>
              · {tn(card.book_entries, '%d lore entry', '%d lore entries')}
            </span>
          ) : null}
          {card.has_phi ? (
            <span class="stage-cardsheet__phi" title={t('Has post-history instructions — a directive appended after the chat history each turn.')}>
              {t('· post-history')}
            </span>
          ) : null}
        </div>

        <div class="stage-cardsheet__start">
          {greetings > 1 && (
            <div class="stage-greeting-pick">
              <button disabled={greeting <= 0} onClick={() => setGreeting((g) => Math.max(0, g - 1))}>
                ◀
              </button>
              <span>
                {greeting + 1}/{greetings}
              </span>
              <button disabled={greeting >= greetings - 1} onClick={() => setGreeting((g) => Math.min(greetings - 1, g + 1))}>
                ▶
              </button>
            </div>
          )}
          <button class="stage-sheet__start" disabled={busy} onClick={() => onStart(greeting)}>
            {t('Start chat')}
          </button>
        </div>

        {findings && findings.length > 0 && (
          <div class="stage-lint">
            <div class="stage-lint__head">
              <span>{t('Findings')}</span>
              <span class="stage-lint__counts">
                {warns > 0 && <span class="stage-lint__count stage-lint__count--warn">⚠ {warns}</span>}
                {infos > 0 && <span class="stage-lint__count stage-lint__count--info">ⓘ {infos}</span>}
              </span>
            </div>
            <ul class="stage-lint__list">
              {findings.map((f, i) => (
                <li key={i} class={`stage-lint__item stage-lint__item--${f.severity === 'warn' ? 'warn' : 'info'}`}>
                  <span class="stage-lint__sev" aria-hidden="true">
                    {f.severity === 'warn' ? '⚠' : 'ⓘ'}
                  </span>
                  <div class="stage-lint__body">
                    <span class="stage-lint__msg">{f.message}</span>
                    {(f.field || f.detail) && (
                      <span class="stage-lint__where">
                        {f.field && <span class="stage-lint__field">{f.field}</span>}
                        {f.detail && <code class="stage-lint__detail">{f.detail}</code>}
                      </span>
                    )}
                  </div>
                </li>
              ))}
            </ul>
          </div>
        )}
        {findings && findings.length === 0 && <p class="stage-lint__clean">{t('✓ No lint findings.')}</p>}

        <div class="stage-cardsheet__fields">
          {!view && !error && <p class="stage-empty">{t('Loading…')}</p>}
          {data && (
            <>
              <Field label={t('Description')} value={data.description} hint={t("The character's core prompt — baked into the cached prefix every turn.")} />
              <Field label={t('Personality')} value={data.personality} hint={t('A short personality summary. Many cards fold this into the description instead.')} />
              <Field label={t('Scenario')} value={data.scenario} hint={t('The situation the roleplay opens in.')} />
              <Field
                label={t('First message')}
                value={greetingText(data, greeting)}
                hint={t('The opening line. Use the greeting picker above to preview alternates.')}
              />
              <Field label={t('Example dialogue')} value={data.mes_example} hint={t("Sample exchanges that teach the character's voice.")} />
              <Field label={t('System prompt')} value={data.system_prompt} hint={t('An override system prompt the card requests, if any.')} />
              <Field
                label={t('Post-history instructions')}
                value={data.post_history_instructions}
                hint={t('Appended AFTER the chat history each turn — strong, recency-weighted steering.')}
              />
              <Field label={t('Creator notes')} value={data.creator_notes} hint={t('Shown to you only — never sent to the model.')} />
              {book.length > 0 && (
                <div class="stage-cardfield">
                  <div class="stage-cardfield__label">
                    {t('Lorebook')}
                    <span class="stage-cardfield__hint" title={t("Keyword-triggered context entries imported onto the session's lore engine.")} aria-label={t('lorebook')}>
                      ⓘ
                    </span>
                  </div>
                  <ul class="stage-cardsheet__book">
                    {book.map((e, i) => (
                      <li key={i}>
                        <span class="stage-cardsheet__book-name">{e.name || e.comment || t('entry %d', i + 1)}</span>
                        {e.constant ? (
                          <span class="stage-cardsheet__book-tag">{t('always on')}</span>
                        ) : (
                          (e.keys ?? []).slice(0, 6).map((k) => (
                            <span key={k} class="stage-cardsheet__book-key">
                              {k}
                            </span>
                          ))
                        )}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </div>

        {onDuplicate && (
          <button class="stage-sheet__duplicate" title={t('Copy this card, portrait and all, so you can change the copy')} onClick={onDuplicate}>
            {t('⧉ Duplicate card')}
          </button>
        )}
        <button class="stage-sheet__export" onClick={() => void exportCard()}>
          {t('Export card')}
        </button>
        {onDelete && (
          <button class="stage-sheet__delete" onClick={onDelete}>
            {t('Delete card')}
          </button>
        )}
      </div>
    </div>
  )
}
