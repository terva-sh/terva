import { useCallback, useContext, useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootMember, TalkootNote, TalkootNotesResult, TalkootRefText } from '../../platform/ctrlproto/types'
import type { Presence } from '../../platform/talkoot/store'
import { humanBytes, localInstant } from '../../ui/formatting'
import { panelHref } from '../../ui/navlinks'
import { MemberCard } from '../../ui/talkoot/MemberCard'
import { OpenRefContext, RefText } from '../../ui/talkoot/RefChip'
import { PRESENCE_LABEL } from '../../ui/talkoot/TalkootTeam'
import { isWorker, WorkerEvents } from '../../ui/talkoot/WorkerEvents'

export type SideTab = 'member' | 'work' | 'notes' | 'file'

// SidePanel is the app's third column: the selected member's card and work,
// the team's notes, and the file a reference names. The member tabs exist only
// while a member's thread is open. A native member's live session stays in the
// control panel, which the Work tab links to.
export function SidePanel({
  client,
  id,
  member,
  presence,
  person,
  canSteer,
  tab,
  fileRef,
  onTab,
  onClose,
  onShowRef,
  onPause,
  onResume,
  onRefresh,
}: {
  client: ClientLike
  id: string
  member?: TalkootMember
  presence: Presence
  person: string
  canSteer: boolean
  tab: SideTab
  fileRef: string
  onTab: (tab: SideTab) => void
  onClose: () => void
  onShowRef: (ref: string) => void
  onPause: () => void
  onResume: () => void
  onRefresh: () => void
}) {
  const tabs: Array<[SideTab, string]> = []
  if (member) tabs.push(['member', t('Member')], ['work', t('Work')])
  tabs.push(['notes', t('Notes')])
  if (fileRef) tabs.push(['file', t('File')])
  const shown = tabs.some(([k]) => k === tab) ? tab : 'notes'

  return (
    <aside class="tka-side" aria-label={t('Details')}>
      <div class="tka-side-head">
        <div class="tka-tabs" role="tablist">
          {tabs.map(([k, label]) => (
            <button key={k} role="tab" class={`tka-tab${shown === k ? ' on' : ''}`} aria-selected={shown === k} onClick={() => onTab(k)}>
              {label}
            </button>
          ))}
        </div>
        <button class="btn sm ghost" aria-label={t('Close the side panel')} onClick={onClose}>
          ✕
        </button>
      </div>
      <div class="tka-side-body" role="tabpanel">
        {shown === 'member' && member && (
          <MemberCard
            key={member.id}
            client={client}
            id={id}
            member={member}
            person={person}
            canSteer={canSteer}
            onPause={onPause}
            onResume={onResume}
          />
        )}
        {shown === 'work' && member && <Work key={member.id} client={client} id={id} member={member} presence={presence} onRefresh={onRefresh} />}
        {shown === 'notes' && <Notes client={client} id={id} onOpen={onShowRef} />}
        {shown === 'file' && fileRef && <FileView key={fileRef} fileRef={fileRef} />}
      </div>
    </aside>
  )
}

// Work is what a member does now. A worker member's events come from its swarm
// agent. A native member runs a session the control panel shows whole.
function Work({
  client,
  id,
  member,
  presence,
  onRefresh,
}: {
  client: ClientLike
  id: string
  member: TalkootMember
  presence: Presence
  onRefresh: () => void
}) {
  const s = member.status
  const native = !isWorker(member)
  // ⚠️ A member's first turn opens its session, and the daemon sends no roster
  // event for it, so a view read before that turn still has no session. One
  // read when the tab opens finds it.
  useEffect(() => {
    if (native && !member.session) onRefresh()
  }, [])
  const rows: Array<[string, string]> = [[t('State'), PRESENCE_LABEL[presence]()]]
  if (s?.tool) rows.push([t('Tool'), s.tool])
  if (s?.paused) rows.push([t('Paused'), s.paused])
  rows.push([t('Spend today'), `$${(s?.spend_usd ?? 0).toFixed(2)}`], [t('Turns'), String(s?.turns ?? 0)])
  if (s?.idle) rows.push([t('Worker'), t('stopped while idle')])
  return (
    <div class="tka-work">
      <dl class="tka-facts">
        {rows.map(([k, v]) => (
          <div key={k}>
            <dt>{k}</dt>
            <dd>{v}</dd>
          </div>
        ))}
      </dl>
      {!native ? (
        <WorkerEvents client={client} id={id} member={member} />
      ) : member.session ? (
        <a class="btn sm" href={panelHref(member.session)} target="_blank" rel="noopener">
          {t('Open the session in the control panel')}
        </a>
      ) : (
        <p class="talkoot-note">{t('This member has no session yet.')}</p>
      )}
    </div>
  )
}

// Notes lists the notes the members wrote for the team, newest first. A note
// opens in the File tab.
function Notes({ client, id, onOpen }: { client: ClientLike; id: string; onOpen: (ref: string) => void }) {
  const [notes, setNotes] = useState<TalkootNote[] | null>(null)
  const [error, setError] = useState('')
  const load = useCallback(() => {
    setError('')
    client.send<TalkootNotesResult>('talkoot.notes', { id }).then(
      (r) => setNotes(r?.notes ?? []),
      (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    )
  }, [client, id])
  useEffect(load, [load])

  return (
    <div class="tka-notes">
      <div class="tka-notes-head">
        <span class="talkoot-note">{t('The notes the members wrote for the team.')}</span>
        <button class="btn sm ghost" onClick={load}>
          {t('Refresh')}
        </button>
      </div>
      {error && <div class="talkoot-error">{error}</div>}
      {notes === null && !error && <span class="talkoot-note">{t('Reading…')}</span>}
      {notes?.length === 0 && <span class="talkoot-note">{t('No member has written a note yet.')}</span>}
      {notes && notes.length > 0 && (
        <ul class="tka-note-list">
          {notes.map((n) => (
            <li key={n.ref}>
              <button class="tka-item" onClick={() => onOpen(n.ref)}>
                <span class="tka-member-text">
                  <span class="tka-item-name">{n.name}</span>
                  <span class="talkoot-note">
                    {n.member} · {humanBytes(n.size)} · {localInstant(n.at)}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// FileView reads one path: or note: reference through the team view's reader.
function FileView({ fileRef }: { fileRef: string }) {
  const open = useContext(OpenRefContext)
  const [result, setResult] = useState<TalkootRefText | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    if (!open) return
    let live = true
    open(fileRef).then(
      (r) => {
        if (!live) return
        setResult(r)
        setLoading(false)
      },
      (e: unknown) => {
        if (!live) return
        setError(e instanceof Error ? e.message : String(e))
        setLoading(false)
      },
    )
    return () => {
      live = false
    }
  }, [open, fileRef])
  return (
    <div class="tka-file">
      <code class="tka-file-ref">{fileRef}</code>
      <RefText result={result} error={error} loading={loading} />
    </div>
  )
}
