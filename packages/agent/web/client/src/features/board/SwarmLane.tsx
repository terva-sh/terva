import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { TaskInfo } from '../../platform/ctrlproto/types'

// SwarmLane is the board's SECOND lane: the workspace's background swarm agents,
// rendered beside the session tiles rather than as sessions — they are a
// deliberately parallel universe (separate processes, foreign wires normalized
// at the runner), which is exactly why the board gives them their own lane
// (docs/proposals/orchestration-frontend.md §4.2). Fed by the tasks surface;
// app.tsx fetches it and routes actions via surface.action.
//
// The worker-shaped fields (backend, cost) render only when an
// external-agent-workers daemon supplies them; against an older daemon they are
// simply absent and the tile hides them, so the lane renders native children
// today and lights up workers with zero board change.
//
// A human can spawn from here too (onSpawn), choosing a backend: `native` (an
// in-house swarm agent, always available) or a foreign worker from `backends`.
// Foreign options grey out when workers are disabled — offerable, so the picker
// stays discoverable, but gated; the daemon applies the SAME gate the model's
// swarm_spawn tool does, and rejects a foreign spawn it wouldn't allow.
export function SwarmLane(props: {
  tasks: TaskInfo[]
  backends?: string[]
  workersEnabled?: boolean
  onAction: (id: string, action: string, args?: Record<string, string>) => void
  onSpawn?: (task: string, backend: string) => void
  // waiting[agentId] = the dispatching session id when that worker is parked on a
  // tool approval (its permission_request rode that session's stream). Present ⇒
  // the tile badges "needs approval"; clicking it opens the session, because the
  // human approval card lives in focus mode, not on the board (phase-B follow-on).
  waiting?: Record<string, string>
  onOpenSession?: (sess: string) => void
}) {
  const [spawning, setSpawning] = useState(false)
  const [task, setTask] = useState('')
  const [backend, setBackend] = useState('') // '' = native swarm agent
  const backends = props.backends ?? []
  const canSpawn = props.onSpawn != null

  // A lane with no agents and no way to add one stays hidden (an old daemon, or a
  // workspace with the swarm off). Once spawning is possible the lane persists so
  // the "+ New" affordance has a home even before the first agent exists.
  if (props.tasks.length === 0 && !canSpawn) return null

  const submit = () => {
    const trimmed = task.trim()
    if (!trimmed) return
    props.onSpawn?.(trimmed, backend)
    setTask('')
    setBackend('')
    setSpawning(false)
  }

  return (
    <div class="board-lane">
      <div class="board-head">
        <strong>{t('Swarm')}</strong>
        <span class="board-lane-count">{props.tasks.length}</span>
        {canSpawn && !spawning && (
          <button class="btn board-head-action" onClick={() => setSpawning(true)}>
            + {t('Spawn')}
          </button>
        )}
      </div>

      {spawning && (
        <div class="swarm-spawn">
          <input
            class="swarm-spawn-task"
            placeholder={t('Task for the new agent…')}
            value={task}
            autofocus
            onInput={(e) => setTask((e.target as HTMLInputElement).value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') submit()
              else if (e.key === 'Escape') setSpawning(false)
            }}
          />
          <select
            class="swarm-spawn-backend"
            value={backend}
            onChange={(e) => setBackend((e.target as HTMLSelectElement).value)}
          >
            <option value="">{t('native')}</option>
            {backends.map((b) => (
              <option key={b} value={b} disabled={!props.workersEnabled}>
                {b}
              </option>
            ))}
          </select>
          <button class="btn sm" onClick={submit} disabled={!task.trim()}>
            {t('Spawn')}
          </button>
          <button class="btn sm" onClick={() => setSpawning(false)}>
            {t('Cancel')}
          </button>
        </div>
      )}
      {spawning && backends.length > 0 && !props.workersEnabled && (
        <div class="swarm-spawn-hint">
          {t('External workers are off — enable external_workers in settings to dispatch one.')}
        </div>
      )}

      {props.tasks.length === 0 ? (
        <div class="board-empty">{t('No swarm agents running.')}</div>
      ) : (
        <div class="board-grid">
          {props.tasks.map((task) => {
            const running = task.status === 'running' || task.status === 'pending'
            const removable =
              task.status === 'done' ||
              task.status === 'failed' ||
              task.status === 'killed' ||
              task.status === 'detached'
            const act = (a: string) => props.onAction('tasks', a, { id: task.id })
            const stallSess = props.waiting?.[task.id]
            return (
              <div key={task.id} class={`board-tile swarm-tile${stallSess ? ' waiting' : ''}`}>
                <div class="board-tile-head">
                  <span class="board-tile-title">{task.task || task.id}</span>
                  <span class={`task-status s-${task.status}`}>{task.status}</span>
                </div>
                {stallSess && (
                  <button
                    class="swarm-waiting"
                    title={t('Waiting on approval — open the session to answer')}
                    onClick={(e) => (e.stopPropagation(), props.onOpenSession?.(stallSess))}
                  >
                    {t('needs approval')}
                  </button>
                )}
                <div class="board-tile-meta">
                  {task.backend && <span class="swarm-backend">{task.backend}</span>}
                  {task.model || ''}
                  {typeof task.cost_usd === 'number' && task.cost_usd > 0
                    ? ' · $' + task.cost_usd.toFixed(4)
                    : ''}
                </div>
                {task.activity && <div class="swarm-activity">{task.activity}</div>}
                {task.error && <div class="swarm-error">{task.error}</div>}
                <div class="board-tile-actions">
                  {running && (
                    <button class="btn sm" onClick={() => act('stop')}>
                      {t('Stop')}
                    </button>
                  )}
                  {task.status === 'detached' && (
                    <button class="btn sm" onClick={() => act('resume')}>
                      {t('Resume')}
                    </button>
                  )}
                  {removable && (
                    <button class="btn sm" onClick={() => act('remove')}>
                      {t('Remove')}
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
