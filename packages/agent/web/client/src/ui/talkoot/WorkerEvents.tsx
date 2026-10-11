import { useEffect, useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { ClientLike } from '../../platform/ctrlproto/client'
import type { TalkootMember, TaskInfo } from '../../platform/ctrlproto/types'

// WORKER_POLL_MS is how often an open event view reads the worker again. The
// read is one small snapshot, and it runs only while the view is open.
export const WORKER_POLL_MS = 2000

// isWorker reports whether a member runs as a swarm worker rather than as a
// native session. The roster names no driver for a native member.
export function isWorker(m: TalkootMember): boolean {
  return !!m.driver && m.driver !== 'native'
}

// WorkerEvents is a worker member's event view: what its swarm agent is
// doing, read with talkoot.worker while the view is open.
export function WorkerEvents({ client, id, member }: { client: ClientLike; id: string; member: TalkootMember }) {
  const [task, setTask] = useState<TaskInfo | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let live = true
    // One read at a time. A tick that finds a read still out skips, so a
    // slow answer can never land after a newer one and replace it.
    let reading = false
    const read = () => {
      if (reading) return
      reading = true
      client.send<TaskInfo>('talkoot.worker', { id, member: member.id }).then(
        (tk) => {
          reading = false
          if (!live) return
          setTask(tk)
          setError('')
        },
        (err: unknown) => {
          reading = false
          if (!live) return
          // The last snapshot stays, with the reason it is no longer fresh.
          setError(err instanceof Error ? err.message : String(err))
        },
      )
    }
    read()
    const timer = setInterval(read, WORKER_POLL_MS)
    return () => {
      live = false
      clearInterval(timer)
    }
  }, [client, id, member.id])
  if (!task) return <div class="talkoot-note">{error || t('Loading the worker’s events…')}</div>
  const facts = [
    task.status,
    task.activity,
    task.turns ? t('%d turns', task.turns) : '',
    task.tool_calls ? t('%d tools', task.tool_calls) : '',
    task.cost_usd ? t('$%s', task.cost_usd.toFixed(2)) : '',
  ].filter(Boolean)
  return (
    <div class="task-row talkoot-worker-events" aria-label={t('Worker events')}>
      <div class="task-row-head">
        <code>{task.id}</code>
        <span>{facts.join(' · ')}</span>
      </div>
      {error && <div class="talkoot-error">{error}</div>}
      {task.error && <div class="talkoot-error">{task.error}</div>}
      {task.tail ? <pre class="task-tail">{task.tail}</pre> : <div class="talkoot-note">{t('The worker has written nothing yet.')}</div>}
    </div>
  )
}
