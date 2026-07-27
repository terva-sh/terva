import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import { CopyButton } from '../../ui/CopyButton'
import { humanBytes, localInstant } from '../../ui/formatting'
import type { WorkflowRunView } from '../../platform/ctrlproto/types'
import { agentsLine } from './WorkflowLane'

// WorkflowRunDetail is one run, opened: what it was, the script it ran, and
// every report it produced.
//
// The script tab is the reason this exists. A workflow's definition used to live
// only in whatever .js file the operator launched from — so reading back what a
// run actually did meant shell access to the host, and a file that may since
// have moved or been edited. The source here is the one recorded AT LAUNCH,
// which is the only copy that answers "what ran".
//
// Results are collapsed by default and labelled with their size. A single
// deliverable in the run this was built for was 98 KB; expanding one is a
// decision, not something the page does to you on open.
export function WorkflowRunDetail(props: {
  view: WorkflowRunView | null
  err: string
  onClose: () => void
}) {
  const [tab, setTab] = useState<'overview' | 'script' | 'results'>('overview')
  const v = props.view
  const results = v?.results ?? []
  return (
    <div class="modal-scrim" onClick={props.onClose}>
      <div class="modal wf-modal" onClick={(e) => e.stopPropagation()}>
        <div class="modal-head">
          <strong>{v?.run.name || v?.run.id || t('Workflow run')}</strong>
          <button class="icon sm" title={t('Close')} onClick={props.onClose}>
            ×
          </button>
        </div>
        {props.err && <div class="pick-empty">{props.err}</div>}
        {!v && !props.err && <div class="pick-empty">{t('loading…')}</div>}
        {v && (
          <>
            <div class="pane-tabs">
              <button
                class={`pane-tab${tab === 'overview' ? ' active' : ''}`}
                onClick={() => setTab('overview')}
              >
                {t('Overview')}
              </button>
              <button
                class={`pane-tab${tab === 'script' ? ' active' : ''}`}
                onClick={() => setTab('script')}
              >
                {t('Script')}
              </button>
              <button
                class={`pane-tab${tab === 'results' ? ' active' : ''}`}
                onClick={() => setTab('results')}
              >
                {t('Results')}
                <span class="pane-tab-badge">{results.length}</span>
              </button>
            </div>
            <div class="wf-body">
              {tab === 'overview' && <Overview view={v} />}
              {tab === 'script' && <Script src={v.script ?? ''} />}
              {tab === 'results' && <Results view={v} />}
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function Overview({ view }: { view: WorkflowRunView }) {
  const r = view.run
  const rows: Array<[string, string]> = [
    [t('Run'), r.id],
    [t('Status'), r.status],
    [t('Agents'), agentsLine(r)],
    [t('Started'), localInstant(r.started)],
  ]
  if (r.ended) rows.push([t('Ended'), localInstant(r.ended)])
  if (r.script_at) rows.push([t('Launched from'), r.script_at])
  if (r.cwd) rows.push([t('Working directory'), r.cwd])
  return (
    <div class="wf-overview">
      <dl class="wf-facts">
        {rows.map(([k, val]) => (
          <div key={k} class="wf-fact">
            <dt>{k}</dt>
            <dd>{val}</dd>
          </div>
        ))}
      </dl>
      {r.error && <div class="wf-error">{r.error}</div>}
      {/* The resume line is the point of the whole record: an interrupted run's
          finished agents are on disk, and re-running the script without --resume
          pays for them again. Copyable because it is a command, not a fact. */}
      {r.resumable && (
        <div class="wf-resume">
          <div class="wf-resume-why">
            {t('This run stopped with finished agents on disk. Resuming replays them instead of paying again.')}
          </div>
          {r.script_at ? (
            <div class="wf-cmd">
              <code>{resumeCommand(r.script_at, r.id)}</code>
              <CopyButton text={resumeCommand(r.script_at, r.id)} inline />
            </div>
          ) : (
            <div class="wf-resume-why">
              {t('This run did not record where it was launched from, so the resume command needs the script path: terva workflow run <script> --resume %s', r.id)}
            </div>
          )}
        </div>
      )}
      {view.args !== undefined && view.args !== null && (
        <div class="wf-args">
          <div class="wf-sub">{t('Args')}</div>
          <pre class="wf-pre">{pretty(view.args)}</pre>
        </div>
      )}
    </div>
  )
}

export function resumeCommand(scriptAt: string, runID: string): string {
  return `terva workflow run ${scriptAt} --resume ${runID}`
}

function Script({ src }: { src: string }) {
  if (!src) {
    return (
      <div class="pick-empty">
        {t('No script recorded — this run predates run records, so only its journal survives.')}
      </div>
    )
  }
  return (
    <div class="wf-script">
      <div class="wf-script-bar">
        <CopyButton text={src} label={t('Copy the script')} inline />
      </div>
      <pre class="wf-pre">{src}</pre>
    </div>
  )
}

function Results({ view }: { view: WorkflowRunView }) {
  const results = view.results ?? []
  if (results.length === 0) {
    return (
      <div class="pick-empty">
        {view.run.status === 'incomplete'
          ? t('Nothing journaled yet. A result appears here when an agent finishes.')
          : t('This run journaled no results.')}
      </div>
    )
  }
  return (
    <div class="wf-results">
      {results.map((r, i) => (
        <ResultRow key={r.agent_id || i} label={r.label} agentId={r.agent_id} bytes={r.bytes} body={r.result} />
      ))}
    </div>
  )
}

function ResultRow(props: {
  label?: string
  agentId?: string
  bytes?: number
  body: unknown
}) {
  const [open, setOpen] = useState(false)
  const text = pretty(props.body)
  return (
    <div class="wf-result">
      <button class="wf-result-head" onClick={() => setOpen((o) => !o)}>
        <span class="wf-result-caret">{open ? '▾' : '▸'}</span>
        <span class="wf-result-label">{props.label || props.agentId || t('(unlabelled)')}</span>
        {props.label && props.agentId && <span class="wf-result-agent">{props.agentId}</span>}
        <span class="wf-result-size">{humanBytes(props.bytes ?? text.length)}</span>
      </button>
      {open && (
        <div class="wf-result-body">
          <div class="wf-script-bar">
            <CopyButton text={text} inline />
          </div>
          <pre class="wf-pre">{text}</pre>
        </div>
      )}
    </div>
  )
}

// A journaled result is raw JSON. A schema deliverable is an object and pretty-
// prints; a plain findings report is a JSON *string*, and printing that as
// `"line one\nline two"` would be unreadable — so a string is unwrapped to the
// text it holds, which is what the agent actually wrote.
export function pretty(v: unknown): string {
  if (typeof v === 'string') return v
  try {
    return JSON.stringify(v, null, 2) ?? ''
  } catch {
    return String(v)
  }
}
