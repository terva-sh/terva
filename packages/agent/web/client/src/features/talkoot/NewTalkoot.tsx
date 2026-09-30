import { useEffect, useRef, useState } from 'preact/hooks'
import { t, tn } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type {
  TalkootPreview,
  TalkootPreviewMember,
  TalkootTemplate,
  TalkootTemplatesResult,
  TalkootView,
} from '../../platform/ctrlproto/types'

const ID = /^[a-z][a-z0-9-]*$/

interface PreviewInputs {
  template: string
  id: string
  budget_usd_per_day?: number
  drop?: string[]
}

// NewTalkoot creates a talkoot from a template. The person picks a template,
// names the team, and reads the preview: every member's driver, posture,
// workspace, and budget. A member this machine cannot run must be dropped.
// Create sends the preview's digest, and the daemon refuses a roster that
// differs from the one shown. Any change to the form clears the preview.
export function NewTalkoot({
  client,
  taken,
  onCreated,
  onCancel,
}: {
  client: ClientLike
  taken: string[]
  onCreated: (id: string) => void
  onCancel: () => void
}) {
  const [templates, setTemplates] = useState<TalkootTemplate[] | null>(null)
  const [template, setTemplate] = useState('')
  const [id, setId] = useState('')
  const [budget, setBudget] = useState('')
  const [drop, setDrop] = useState<string[]>([])
  // The preview, and the exact inputs it was made from. Create sends those
  // inputs with its digest, never the form's current ones.
  const [preview, setPreview] = useState<{ result: TalkootPreview; params: PreviewInputs } | null>(null)
  // Counts preview requests. A response for any but the newest is dropped, so
  // a slow answer to an old form cannot land after an edit.
  const seq = useRef(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    client.send<TalkootTemplatesResult>('talkoot.templates', null).then(
      (r) => {
        setTemplates(r.templates)
        const first = r.templates.find((x) => !x.problem)
        if (first) setTemplate(first.name)
      },
      (e: unknown) => setError(e instanceof Error ? e.message : String(e)),
    )
  }, [client])

  // Any edit to the form makes the preview stale, including one still in
  // flight. A drop re-runs it instead, so dropping several members is one
  // click each.
  // The dropped request never answers here, so it releases busy itself.
  useEffect(() => {
    seq.current++
    setPreview(null)
    setBusy(false)
  }, [template, id, budget])

  const idProblem = !id ? '' : !ID.test(id) ? t('Use lower case letters, digits, and dashes, starting with a letter.') : taken.includes(id) ? t('A talkoot with this name exists.') : ''
  const budgetValue = budget.trim() === '' ? 0 : Number(budget)
  const budgetProblem = budget.trim() !== '' && !(budgetValue > 0) ? t('The budget must be a positive number.') : ''
  const ready = !!template && !!id && !idProblem && !budgetProblem

  const params = (d = drop): PreviewInputs => ({ template, id, budget_usd_per_day: budgetValue || undefined, drop: d.length ? d : undefined })

  const runPreview = (d = drop) => {
    const mine = ++seq.current
    const inputs = params(d)
    setBusy(true)
    setPreview(null)
    client.send<TalkootPreview>('talkoot.preview', inputs).then(
      (p) => {
        if (mine !== seq.current) return
        setPreview({ result: p, params: inputs })
        setError('')
        setBusy(false)
      },
      (e: unknown) => {
        if (mine !== seq.current) return
        setError(e instanceof Error ? e.message : String(e))
        setBusy(false)
      },
    )
  }

  const create = () => {
    if (!preview) return
    setBusy(true)
    client.send<TalkootView>('talkoot.create', { ...preview.params, digest: preview.result.digest }).then(
      (v) => onCreated(v.id),
      (e: unknown) => {
        setError(e instanceof Error ? e.message : String(e))
        setBusy(false)
      },
    )
  }

  const blocked = preview ? preview.result.members.some((m) => !m.available) || (preview.result.problems?.length ?? 0) > 0 : true
  const picked = templates?.find((x) => x.name === template)

  return (
    <div class="talkoot-new">
      <h2>{t('New team from a template')}</h2>
      {error && <div class="talkoot-error">{error}</div>}
      <label class="talkoot-field">
        <span>{t('Template')}</span>
        <select value={template} onChange={(e) => { setTemplate((e.target as HTMLSelectElement).value); setDrop([]) }}>
          {(templates ?? []).map((x) => (
            <option key={x.name} value={x.name} disabled={!!x.problem}>
              {x.title || x.name} ({sourceLabel(x.source)}){x.problem ? ` — ${t('cannot be used')}` : ''}
            </option>
          ))}
        </select>
      </label>
      {picked && (
        <p class="talkoot-note">
          {picked.description} {tn(picked.members, '%d member.', '%d members.')}
          {picked.source === 'repo' && ` ${t('This template comes from the repository. Read every member below before you create the team.')}`}
        </p>
      )}
      {templates?.filter((x) => x.problem).map((x) => (
        <p key={x.name} class="talkoot-note">
          {x.name}: {x.problem}
        </p>
      ))}
      <label class="talkoot-field">
        <span>{t('Name')}</span>
        <input value={id} placeholder={t('team')} onInput={(e) => setId((e.target as HTMLInputElement).value.trim())} />
      </label>
      {idProblem && <p class="talkoot-error">{idProblem}</p>}
      <label class="talkoot-field">
        <span>{t('Daily budget (USD)')}</span>
        <input
          value={budget}
          inputMode="decimal"
          placeholder={t('the template suggests one')}
          onInput={(e) => setBudget((e.target as HTMLInputElement).value)}
        />
      </label>
      {budgetProblem && <p class="talkoot-error">{budgetProblem}</p>}
      <div class="card-actions">
        <button class="btn" disabled={!ready || busy} onClick={() => runPreview()}>
          {t('Preview')}
        </button>
        <button class="btn ghost" onClick={onCancel}>
          {t('Cancel')}
        </button>
      </div>
      {preview && (
        <PreviewTable
          preview={preview.result}
          drop={drop}
          onToggleDrop={(m) => {
            const next = drop.includes(m) ? drop.filter((x) => x !== m) : [...drop, m]
            setDrop(next)
            runPreview(next)
          }}
        />
      )}
      {preview && (
        <div class="card-actions">
          <button class="btn primary" disabled={blocked || busy} onClick={create}>
            {t('Create the team')}
          </button>
          {blocked && <span class="talkoot-note">{t('Drop each member that cannot run here, and fix every problem, then preview again.')}</span>}
        </div>
      )}
    </div>
  )
}

function sourceLabel(source: string): string {
  if (source === 'builtin') return t('built in')
  if (source === 'user') return t('yours')
  if (source === 'repo') return t('this repository')
  return source.startsWith('ext:') ? t('extension %s', source.slice(4)) : source
}

// PreviewTable lists each member the roster would write, with the defaults
// applied, so a person sees every driver, posture, and budget.
function PreviewTable({
  preview,
  drop,
  onToggleDrop,
}: {
  preview: TalkootPreview
  drop: string[]
  onToggleDrop: (member: string) => void
}) {
  return (
    <div class="talkoot-preview">
      <p>
        {t('Runs in %s with a daily budget of $%s.', preview.home, String(preview.budget_usd_per_day))}
      </p>
      <table>
        <thead>
          <tr>
            <th>{t('Member')}</th>
            <th>{t('Role')}</th>
            <th>{t('Driver')}</th>
            <th>{t('Model')}</th>
            <th>{t('Posture')}</th>
            <th>{t('Workspace')}</th>
            <th>{t('Limits')}</th>
            <th>{t('Drop')}</th>
          </tr>
        </thead>
        <tbody>
          {preview.members.map((m) => (
            <PreviewRow key={m.id} m={m} onDrop={() => onToggleDrop(m.id)} />
          ))}
          {drop.map((id) => (
            <tr key={id} class="dropped">
              <td>
                <code>{id}</code>
              </td>
              <td colSpan={6}>{t('dropped')}</td>
              <td>
                <input type="checkbox" checked aria-label={t('Keep %s', id)} onChange={() => onToggleDrop(id)} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {preview.problems && preview.problems.length > 0 && (
        <ul class="talkoot-problems">
          {preview.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      <details>
        <summary>{t('The talkoot.md this writes')}</summary>
        <pre class="preview">{preview.text}</pre>
      </details>
    </div>
  )
}

function PreviewRow({ m, onDrop }: { m: TalkootPreviewMember; onDrop: () => void }) {
  const limits = [
    m.budget_usd_per_day ? `$${m.budget_usd_per_day}/day` : '',
    m.turns_per_day ? tn(m.turns_per_day, '%d turn/day', '%d turns/day') : '',
    m.tools ? t('tools: %s', m.tools.join(', ')) : '',
  ].filter(Boolean)
  return (
    <tr class={m.available ? '' : 'unavailable'}>
      <td>
        <code>{m.id}</code>
        {m.title ? ` ${m.title}` : ''}
        {m.reviewer ? ` · ${t('reviewer')}` : ''}
      </td>
      <td>{m.role}</td>
      <td>
        {m.driver}
        {!m.available && <div class="talkoot-error">{m.problem}</div>}
      </td>
      <td>{m.model || m.tier || ''}</td>
      <td class={m.posture === 'plan' ? '' : 'writes'}>{m.posture}</td>
      <td>{m.workspace}</td>
      <td>{limits.join(', ')}</td>
      <td>
        <input type="checkbox" checked={false} aria-label={t('Drop %s', m.id)} onChange={onDrop} />
      </td>
    </tr>
  )
}
