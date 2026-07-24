import { useEffect, useState } from 'preact/hooks'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type {
  CardHistoryResult,
  CardRevision,
  CardRevisionView,
  CardView,
} from '../../platform/ctrlproto/types'
import { t, tn, tr } from '../../i18n'
import { PORTRAIT_FIELD, fieldChanges, fieldLabel, type FieldChange } from './carddiff'

// The character's earlier versions, inside the editor that produced them.
//
// Hand-editing is recoverable because you remember what you wrote. The card
// doctor and enrich rewrite fields you never typed — and the doctor proposes
// removals — so the state worth getting back may be one you never read. The
// server records the OUTGOING card on every cards.edit, which is why the first
// edit of any card captures it exactly as imported.
//
// Two ways back, and the difference between them is the whole design here:
//
//   - RESTORE puts the whole revision back. It writes immediately, and is safe
//     to try because the server records what it replaced — a restore is itself
//     undoable. The only thing it can destroy is an unsaved draft, which is
//     what the confirm step guards.
//   - USE THIS copies ONE field into the editor as a draft. It writes nothing;
//     you review it beside everything else and press Save. That is why it is
//     offered only for fields the editor renders — see REVERTABLE_FIELDS.

// whenLabel renders a revision's age. Exported for its own test: the buckets
// are what make a list of stamps readable at a glance, and they are easy to get
// subtly wrong (an off-by-one that reports "0 minutes ago").
export function whenLabel(saved: string, now: number): string {
  const then = Date.parse(saved)
  if (!Number.isFinite(then)) return ''
  const secs = Math.max(0, Math.round((now - then) / 1000))
  if (secs < 60) return t('just now')
  const mins = Math.floor(secs / 60)
  if (mins < 60) return tn(mins, '%d minute ago', '%d minutes ago', mins)
  const hours = Math.floor(mins / 60)
  if (hours < 24) return tn(hours, '%d hour ago', '%d hours ago', hours)
  const days = Math.floor(hours / 24)
  if (days < 7) return tn(days, '%d day ago', '%d days ago', days)
  return new Date(then).toLocaleDateString()
}

// changedLabel summarizes a revision at a glance. Naming the fields beats a
// count while the list is short — "personality, greeting" is actionable where
// "2 fields" sends you looking.
export function changedLabel(fields: readonly string[] | undefined, portrait?: boolean): string {
  // The portrait is not a card field, but it is a difference — a revision that
  // replaced only the picture would otherwise be labelled "identical to now",
  // which is exactly the silence this whole feature is about.
  const f = [...(fields ?? []), ...(portrait ? [PORTRAIT_FIELD] : [])]
  if (f.length === 0) return t('identical to now')
  if (f.length <= 3) return f.map((x) => tr(fieldLabel(x))).join(', ')
  return tn(f.length, '%d field differs', '%d fields differ', f.length)
}

export function CardHistory(props: {
  client: ClientLike
  cardId: string
  currentName: string
  /** The card as SAVED (the editor's rawDoc.data) — what a revision is compared against. */
  currentData: Record<string, unknown>
  /** Bumped by the editor whenever the stored card moves, so the list re-reads. */
  reloadKey?: number
  onRestored: (v: CardView) => void
  /** Copy one field of a revision into the editor as an unsaved draft. */
  onUseField: (field: string, value: unknown) => void
}) {
  const { client, cardId, currentName, currentData, reloadKey, onRestored, onUseField } = props
  const [revisions, setRevisions] = useState<CardRevision[] | null>(null)
  const [confirming, setConfirming] = useState('')
  const [restoring, setRestoring] = useState('')
  const [open, setOpen] = useState('')
  const [detail, setDetail] = useState<CardRevisionView | null>(null)
  const [loadingDetail, setLoadingDetail] = useState(false)
  const [used, setUsed] = useState<Record<string, boolean>>({})
  const [error, setError] = useState('')

  useEffect(() => {
    setConfirming('')
    setOpen('')
    setDetail(null)
    setUsed({})
    if (!cardId) {
      setRevisions(null)
      return
    }
    let live = true
    client
      .send<CardHistoryResult>('cards.history', { id: cardId })
      .then((r) => {
        if (live) setRevisions(r.revisions ?? [])
      })
      // A daemon that predates this verb answers "unsupported". Null means "say
      // nothing at all" rather than showing an error for a feature the server
      // simply does not have — the same graceful-degradation this editor already
      // uses for models.default_for.
      .catch(() => {
        if (live) setRevisions(null)
      })
    return () => {
      live = false
    }
  }, [cardId, reloadKey])

  // Expanding fetches the one revision the reader asked about. The list carries
  // WHICH fields differ but not their contents, so a card with a large lorebook
  // does not ship ten copies of it to render a list nobody has opened.
  const expand = async (ref: string) => {
    if (open === ref) {
      setOpen('')
      return
    }
    setOpen(ref)
    setDetail(null)
    setError('')
    setLoadingDetail(true)
    try {
      const v = await client.send<CardRevisionView>('cards.revision', { id: cardId, ref })
      setDetail(v)
    } catch (e) {
      setError(String(e))
      setOpen('')
    } finally {
      setLoadingDetail(false)
    }
  }

  const restore = async (ref: string) => {
    setRestoring(ref)
    setError('')
    try {
      const v = await client.send<CardView>('cards.restore', { id: cardId, ref })
      setConfirming('')
      setOpen('')
      setDetail(null)
      onRestored(v)
      const r = await client.send<CardHistoryResult>('cards.history', { id: cardId })
      setRevisions(r.revisions ?? [])
    } catch (e) {
      setError(String(e))
    } finally {
      setRestoring('')
    }
  }

  const useField = (c: FieldChange) => {
    onUseField(c.field, c.value)
    setUsed((u) => ({ ...u, [c.field]: true }))
  }

  if (!cardId || revisions === null) return null

  const detailRows = (rev: CardRevision): FieldChange[] => {
    if (!detail || detail.ref !== rev.ref) return []
    const data = ((detail.raw as { data?: Record<string, unknown> } | null)?.data ?? {}) as Record<string, unknown>
    // The server's field list wins over anything recomputed here, so the rows
    // and the collapsed row's label can never disagree.
    const portrait = detail.portrait ?? rev.portrait
    const fields = [...(detail.fields ?? rev.fields ?? []), ...(portrait ? [PORTRAIT_FIELD] : [])]
    return fieldChanges(data, currentData, fields)
  }

  return (
    <div class="stage-cardhistory">
      <div class="stage-cardhistory__head">
        <span>{t('Earlier versions')}</span>
        {revisions.length > 0 && <span class="stage-cardhistory__count">{revisions.length}</span>}
      </div>
      {error && (
        <p class="stage-error" onClick={() => setError('')}>
          {error}
        </p>
      )}
      {revisions.length === 0 ? (
        <p class="stage-cardhistory__empty">
          {t('Saved changes are kept here, so an edit or a doctor pass can be undone.')}
        </p>
      ) : (
        <ul class="stage-cardhistory__list">
          {revisions.map((rev) => (
            <li key={rev.ref} class="stage-cardhistory__item">
              <div class="stage-cardhistory__row">
                <button
                  class="stage-cardhistory__what"
                  aria-expanded={open === rev.ref}
                  onClick={() => void expand(rev.ref)}
                >
                  <span class="stage-cardhistory__caret" aria-hidden="true">
                    {open === rev.ref ? '▾' : '▸'}
                  </span>
                  <span class="stage-cardhistory__when">{whenLabel(rev.saved, Date.now())}</span>
                  <span class="stage-cardhistory__changed">{changedLabel(rev.fields, rev.portrait)}</span>
                  {rev.name && rev.name !== currentName && (
                    <span class="stage-cardhistory__name">{t('named %s', rev.name)}</span>
                  )}
                </button>
                {confirming === rev.ref ? (
                  <div class="stage-cardhistory__confirm">
                    <span class="stage-cardhistory__warn">{t('Replaces what is in the editor now.')}</span>
                    <button
                      class="stage-cardhistory__yes"
                      disabled={restoring !== ''}
                      onClick={() => void restore(rev.ref)}
                    >
                      {restoring === rev.ref ? t('Restoring…') : t('Restore all')}
                    </button>
                    <button class="stage-cardhistory__no" onClick={() => setConfirming('')}>
                      {t('Cancel')}
                    </button>
                  </div>
                ) : (
                  <button class="stage-cardhistory__restore" onClick={() => setConfirming(rev.ref)}>
                    {t('Restore all')}
                  </button>
                )}
              </div>
              {open === rev.ref && (
                <div class="stage-cardhistory__detail">
                  {loadingDetail && <p class="stage-cardhistory__empty">{t('Loading…')}</p>}
                  {!loadingDetail && detailRows(rev).length === 0 && (
                    <p class="stage-cardhistory__empty">{t('Nothing differs from the card as saved.')}</p>
                  )}
                  {detailRows(rev).map((c) => (
                    <div key={c.field} class="stage-cardhistory__field">
                      <div class="stage-cardhistory__fieldhead">
                        <span class="stage-cardhistory__fieldname">{tr(c.label)}</span>
                        {c.revertable ? (
                          <button
                            class="stage-cardhistory__use"
                            disabled={used[c.field]}
                            onClick={() => useField(c)}
                          >
                            {used[c.field] ? t('Copied ✓') : t('Use this')}
                          </button>
                        ) : (
                          // The editor does not render this field, so copying it
                          // alone would change the saved card with nothing on
                          // screen to show it. A whole restore still can.
                          <span class="stage-cardhistory__only">{t('restore all to change')}</span>
                        )}
                      </div>
                      {(c.before || c.after) && (
                        <div class="stage-cardhistory__pair">
                          <div class="stage-cardhistory__side">
                            <span class="stage-cardhistory__sidelabel">{t('then')}</span>
                            <p class="stage-cardhistory__value">{c.before || t('(empty)')}</p>
                          </div>
                          <div class="stage-cardhistory__side">
                            <span class="stage-cardhistory__sidelabel">{t('now')}</span>
                            <p class="stage-cardhistory__value">{c.after || t('(empty)')}</p>
                          </div>
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
