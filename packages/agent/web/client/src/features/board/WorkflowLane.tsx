import { t, tn } from '../../i18n'
import { localInstant } from '../../ui/formatting'
import type { WorkflowRunInfo } from '../../platform/ctrlproto/types'

// WorkflowLane is the board's THIRD lane: the workflow runs this host has on
// disk. It sits beside the sessions and the swarm because it is the same kind of
// thing — work happening (or having happened) that you want to watch without
// opening it.
//
// It is a lane of RECORDS, not of live processes, and that distinction is the
// honest one. `terva workflow run` is a separate foreground process with its own
// terminal; the daemon serving this board never sees it. What it can see is what
// the run leaves behind, and until now that was reachable only by ssh-ing to the
// host and reading JSON out of the terva home. So a run reads "incomplete" here
// whether it is mid-flight or died an hour ago — a bare pid lies after reuse, and
// claiming "running" about a dead run would be worse than saying nothing.
//
// Pure presentation, like the other two lanes: app.tsx owns the fetch and the
// verbs; this renders WorkflowRunInfo[] and emits intent.
export function WorkflowLane(props: {
  runs: WorkflowRunInfo[]
  onOpen: (id: string) => void
  onRefresh?: () => void
}) {
  return (
    <div class="board-lane">
      <div class="board-head">
        <strong>{t('Workflows')}</strong>
        <span class="board-lane-count">{props.runs.length}</span>
        {props.onRefresh && (
          <button class="btn board-head-action" onClick={props.onRefresh}>
            {t('Refresh')}
          </button>
        )}
      </div>
      {props.runs.length === 0 ? (
        <div class="board-empty">
          {t('No workflow runs on this host yet. `terva workflow run <script.js>` leaves one here.')}
        </div>
      ) : (
        <div class="board-grid">
          {props.runs.map((r) => (
            <div
              key={r.id}
              class="board-tile workflow-tile"
              role="button"
              tabIndex={0}
              // Both, because the tile shows one truncated: the name is the
              // title line and the id is what every CLI verb takes.
              title={r.name ? `${r.name} · ${r.id}` : r.id}
              onClick={() => props.onOpen(r.id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  props.onOpen(r.id)
                }
              }}
            >
              <div class="board-tile-head">
                <span class="board-tile-title">{r.name || r.id}</span>
                <span class={`board-status ${statusClass(r.status)}`}>{statusLabel(r.status)}</span>
              </div>
              <div class="board-tile-meta">{agentsLine(r)}</div>
              <div class="board-tile-meta">{localInstant(r.started)}</div>
              {r.resumable && (
                <div class="board-tile-meta wf-resumable">
                  {tn(
                    r.completed,
                    'resumable — %d finished agent would replay',
                    'resumable — %d finished agents would replay',
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// The status pill reuses the board's three states rather than inventing colours:
// a finished run is `live`-green, a failed one is warned, and an unfinished one
// is the neutral `cold` — which is exactly what "we cannot tell" should look
// like.
function statusClass(s: WorkflowRunInfo['status']): string {
  return s === 'done' ? 'live' : s === 'failed' ? 'failed' : 'cold'
}

function statusLabel(s: WorkflowRunInfo['status']): string {
  return s === 'done' ? t('done') : s === 'failed' ? t('failed') : t('incomplete')
}

// "1/6 agents" is the whole finding when a run stopped early. The total is only
// known once the run closed its record, so an interrupted one shows the count it
// does have rather than a fraction with a zero denominator — and that bare count
// takes a plural, because the single most common value it holds is 1.
export function agentsLine(r: WorkflowRunInfo): string {
  const parts = [
    r.agents
      ? t('%s agents', `${r.completed}/${r.agents}`)
      : tn(r.completed, '%d agent', '%d agents'),
  ]
  if (r.cached) parts.push(t('%d replayed', r.cached))
  if (r.cost_usd) parts.push('$' + r.cost_usd.toFixed(4))
  return parts.join(' · ')
}
