import { errText } from '../../platform/ctrlproto/errors'
import { useEffect, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { RealizeCharacter, RealizeLore, RealizeProposal, RealizeResult } from '../../platform/ctrlproto/types'
import { t, tn } from '../../i18n'
import { ModelPick } from './ModelPick'

// Realize (creator C3 — docs/plans/creator-realize.md): the exit from a
// cartographer conversation. On open it runs a PROPOSE — one Kartoittaja call
// re-reads the whole conversation and extracts a World, the protagonist, the NPC
// roster, the lore, and a cold open. The author edits it here and COMMITS, which
// seeds a play session. Two phases, nothing created until commit — the doctor
// family's rule.
//
// Built for volume (DR-3): a realize is a World + protagonist + ~5 cards + ~25
// lore entries. Everything is accepted by default and edited or dropped in
// place; the lore list is collapsed so it does not bury the rest.

export function RealizeSheet(props: {
  client: ClientLike
  sessionId: string
  defaultProvider?: string
  defaultModel?: string
  onOpenSession: (session: string) => void
  onClose: () => void
}) {
  const { client, sessionId } = props
  const [drafting, setDrafting] = useState(true)
  const [committing, setCommitting] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [p, setP] = useState<RealizeProposal | null>(null)
  const [ovProvider, setOvProvider] = useState('')
  const [ovModel, setOvModel] = useState('')
  const [loreOpen, setLoreOpen] = useState(false)

  const draft = () => {
    setDrafting(true)
    setError('')
    client
      .send<RealizeResult>('sessions.realize', { provider: ovProvider, model: ovModel }, sessionId)
      .then((r) => {
        setP(r.proposal ?? null)
        setNote(r.proposal?.note ?? '')
      })
      .catch((e: unknown) => setError(errText(e)))
      .finally(() => setDrafting(false))
  }
  useEffect(() => {
    draft()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Immutable updaters over the single proposal object.
  const patch = (next: Partial<RealizeProposal>) => setP((cur) => (cur ? { ...cur, ...next } : cur))
  const patchChar = (i: number, next: Partial<RealizeCharacter>) =>
    setP((cur) => (cur ? { ...cur, roster: (cur.roster ?? []).map((c, j) => (j === i ? { ...c, ...next } : c)) } : cur))
  const dropChar = (i: number) => setP((cur) => (cur ? { ...cur, roster: (cur.roster ?? []).filter((_, j) => j !== i) } : cur))
  const patchLore = (i: number, next: Partial<RealizeLore>) =>
    setP((cur) => (cur ? { ...cur, lore: (cur.lore ?? []).map((e, j) => (j === i ? { ...e, ...next } : e)) } : cur))
  const dropLore = (i: number) => setP((cur) => (cur ? { ...cur, lore: (cur.lore ?? []).filter((_, j) => j !== i) } : cur))

  const roster = p?.roster ?? []
  const lore = p?.lore ?? []
  const ready = !!p && !!p.world?.name.trim() && !!p.protagonist?.name.trim() && !!p.cold_open?.trim()

  const commit = () => {
    if (!p) return
    setCommitting(true)
    setError('')
    client
      .send<RealizeResult>('sessions.realize', { commit: true, proposal: p }, sessionId)
      .then((r) => {
        if (r.session?.id) props.onOpenSession(r.session.id)
        props.onClose()
      })
      .catch((e: unknown) => setError(errText(e)))
      .finally(() => setCommitting(false))
  }

  return (
    <div class="stage-sheet-backdrop" onClick={props.onClose}>
      <div class="stage-realize" onClick={(e) => e.stopPropagation()}>
        <header class="stage-realize__head">
          <h3>{t('Realize this world')}</h3>
          <button class="stage-drawer__close" onClick={props.onClose}>✕</button>
        </header>

        {drafting ? (
          <p class="stage-hint">{t('The cartographer is reading the whole conversation…')}</p>
        ) : !p || !p.world?.name ? (
          <>
            <p class="stage-empty">{note || t('There is nothing to realize yet — keep talking it through first.')}</p>
            <div class="stage-realize__acts">
              <button disabled={drafting} onClick={draft}>{t('Try again')}</button>
              <button onClick={props.onClose}>{t('Close')}</button>
            </div>
          </>
        ) : (
          <>
            <p class="stage-hint">
              {t('This is what the cartographer drew from your conversation. Edit anything, drop what you don’t want, then start playing. Nothing is created until you do.')}
            </p>
            {note && <p class="stage-doctor__note">{note}</p>}

            <section class="stage-realize__section">
              <h4>{t('World')}</h4>
              <input
                class="stage-realize__title"
                value={p.world.name}
                placeholder={t('World name')}
                onInput={(e) => patch({ world: { ...p.world, name: (e.target as HTMLInputElement).value } })}
              />
              <textarea
                rows={2}
                value={p.world.description ?? ''}
                placeholder={t('A line or two about this world')}
                onInput={(e) => patch({ world: { ...p.world, description: (e.target as HTMLTextAreaElement).value } })}
              />
            </section>

            <section class="stage-realize__section">
              <h4>{t('You play')}</h4>
              <input
                value={p.protagonist.name}
                placeholder={t('Your character’s name')}
                onInput={(e) => patch({ protagonist: { ...p.protagonist, name: (e.target as HTMLInputElement).value } })}
              />
              <textarea
                rows={2}
                value={p.protagonist.description ?? ''}
                placeholder={t('Who they are')}
                onInput={(e) => patch({ protagonist: { ...p.protagonist, description: (e.target as HTMLTextAreaElement).value } })}
              />
              {p.protagonist.notes && <p class="stage-realize__notes">{p.protagonist.notes}</p>}
            </section>

            <section class="stage-realize__section">
              <h4>{tn(roster.length, 'The cast (%d character)', 'The cast (%d characters)', roster.length)}</h4>
              {roster.length === 0 && <p class="stage-empty">{t('No supporting cast — the scene opens with just you.')}</p>}
              {roster.map((c, i) => (
                <div key={i} class="stage-realize__card">
                  <div class="stage-realize__card-head">
                    <input
                      class="stage-realize__card-name"
                      value={c.name}
                      onInput={(e) => patchChar(i, { name: (e.target as HTMLInputElement).value })}
                    />
                    {c.name === p.cold_open_actor && <span class="stage-realize__opener" title={t('Opens the scene')}>🎬</span>}
                    <button class="stage-realize__drop" title={t('Remove %s', c.name)} onClick={() => dropChar(i)}>✕</button>
                  </div>
                  {c.role && <p class="stage-realize__role">{c.role}</p>}
                  <textarea
                    rows={2}
                    value={c.description ?? ''}
                    placeholder={t('Description')}
                    onInput={(e) => patchChar(i, { description: (e.target as HTMLTextAreaElement).value })}
                  />
                  <textarea
                    rows={2}
                    value={c.personality ?? ''}
                    placeholder={t('Personality')}
                    onInput={(e) => patchChar(i, { personality: (e.target as HTMLTextAreaElement).value })}
                  />
                </div>
              ))}
            </section>

            <section class="stage-realize__section">
              <h4>{t('Cold open')}</h4>
              <textarea
                rows={4}
                value={p.cold_open ?? ''}
                placeholder={t('The first beat of the scene')}
                onInput={(e) => patch({ cold_open: (e.target as HTMLTextAreaElement).value })}
              />
            </section>

            <section class="stage-realize__section">
              <button class="stage-realize__lore-toggle" onClick={() => setLoreOpen((v) => !v)}>
                {loreOpen ? '▾' : '▸'} {tn(lore.length, '%d lore entry', '%d lore entries', lore.length)}
              </button>
              {loreOpen &&
                lore.map((e, i) => (
                  <div key={i} class="stage-realize__lore">
                    <div class="stage-realize__card-head">
                      <input
                        class="stage-realize__card-name"
                        value={e.name}
                        onInput={(ev) => patchLore(i, { name: (ev.target as HTMLInputElement).value })}
                      />
                      {e.always_on && <span class="stage-realize__always" title={t('Always in context')}>📌</span>}
                      <button class="stage-realize__drop" title={t('Remove %s', e.name)} onClick={() => dropLore(i)}>✕</button>
                    </div>
                    <textarea
                      rows={2}
                      value={e.content}
                      onInput={(ev) => patchLore(i, { content: (ev.target as HTMLTextAreaElement).value })}
                    />
                    {e.keys && e.keys.length > 0 && <p class="stage-realize__keys">{t('keys: %s', e.keys.join(', '))}</p>}
                  </div>
                ))}
            </section>

            {(p.given_by_author?.length || p.invented_by_you?.length) && (
              <details class="stage-realize__ledger">
                <summary>{t('What you gave, and what the cartographer supplied')}</summary>
                {p.given_by_author && p.given_by_author.length > 0 && (
                  <p><strong>{t('Yours:')}</strong> {p.given_by_author.join('; ')}</p>
                )}
                {p.invented_by_you && p.invented_by_you.length > 0 && (
                  <p><strong>{t('Invented:')}</strong> {p.invented_by_you.join('; ')}</p>
                )}
              </details>
            )}

            <div class="stage-realize__model">
              <ModelPick
                client={client}
                sessionId={sessionId}
                currentProvider={ovProvider}
                currentModel={ovModel}
                onSelect={(pr, m) => {
                  setOvProvider(pr)
                  setOvModel(m)
                }}
                defaultLabel={t('Session model')}
                defaultProvider={props.defaultProvider}
                defaultModel={props.defaultModel}
              />
            </div>

            <div class="stage-realize__acts">
              <button class="stage-realize__go" disabled={committing || !ready} onClick={commit}>
                {committing ? t('Opening…') : t('Start playing')}
              </button>
              <button disabled={drafting || committing} onClick={draft} title={t('Re-read the conversation and draw it again')}>
                {t('Draw again')}
              </button>
              <button disabled={committing} onClick={props.onClose}>{t('Cancel')}</button>
            </div>
          </>
        )}
        {error && <p class="stage-doctor__error">{error}</p>}
      </div>
    </div>
  )
}
