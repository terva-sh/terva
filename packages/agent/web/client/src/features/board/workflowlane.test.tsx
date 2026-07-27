// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { WorkflowRunInfo, WorkflowRunView } from '../../platform/ctrlproto/types'
import { WorkflowLane, agentsLine } from './WorkflowLane'
import { WorkflowRunDetail, pretty, resumeCommand } from './WorkflowRunDetail'

const run = (o: Partial<WorkflowRunInfo> = {}): WorkflowRunInfo => ({
  id: 'wf_0000000000aa',
  name: 'review',
  status: 'done',
  started: '2026-07-26T10:00:00Z',
  completed: 6,
  agents: 6,
  ...o,
})

const view = (o: Partial<WorkflowRunView> = {}): WorkflowRunView => ({ run: run(), ...o })

const noop = () => {}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('WorkflowLane', () => {
  it('teaches the feature when the host has no runs yet', () => {
    render(<WorkflowLane runs={[]} onOpen={noop} />)
    expect(screen.getByText(/No workflow runs on this host yet/)).toBeTruthy()
  })

  // The lane's whole job in one line: how much finished work is sitting on disk.
  it('shows completed-of-total, because "1/6" is the finding', () => {
    expect(agentsLine(run({ completed: 1, agents: 6 }))).toContain('1/6')
  })

  // The total is only written when a run closes its record, so an interrupted
  // run must not render "1/0".
  it('shows a bare count when the run never recorded a total', () => {
    const line = agentsLine(run({ completed: 1, agents: undefined }))
    // Singular, because 1 is the most common value this holds — an interrupted
    // run that got one agent done is the whole case for the lane.
    expect(line).toContain('1 agent')
    expect(line).not.toContain('1 agents')
    expect(line).not.toContain('/')
    expect(agentsLine(run({ completed: 3, agents: undefined }))).toContain('3 agents')
  })

  it('names the cost and the replayed count when the run has them', () => {
    const line = agentsLine(run({ cached: 2, cost_usd: 3.3671 }))
    expect(line).toContain('2 replayed')
    expect(line).toContain('$3.3671')
  })

  // An unfinished run is never "running": the daemon cannot tell a live run from
  // a crashed one, and the neutral pill is what "we cannot tell" should look like.
  it('renders an unfinished run as incomplete, not running', () => {
    const { container } = render(<WorkflowLane runs={[run({ status: 'incomplete' })]} onOpen={noop} />)
    expect(screen.getByText('incomplete')).toBeTruthy()
    expect(container.querySelector('.board-status.cold')).not.toBeNull()
    expect(screen.queryByText('running')).toBeNull()
  })

  it('flags a resumable run with what resuming would replay', () => {
    render(<WorkflowLane runs={[run({ status: 'incomplete', completed: 1, resumable: true })]} onOpen={noop} />)
    expect(screen.getByText('resumable — 1 finished agent would replay')).toBeTruthy()
  })

  it('opens a run by id when its tile is clicked', () => {
    const onOpen = vi.fn()
    const { container } = render(<WorkflowLane runs={[run()]} onOpen={onOpen} />)
    fireEvent.click(container.querySelector('.workflow-tile')!)
    expect(onOpen).toHaveBeenCalledWith('wf_0000000000aa')
  })

  // A run with no meta.name still has to be identifiable.
  it('falls back to the run id when the script had no name', () => {
    render(<WorkflowLane runs={[run({ name: undefined })]} onOpen={noop} />)
    expect(screen.getByText('wf_0000000000aa')).toBeTruthy()
  })
})

describe('WorkflowRunDetail', () => {
  it('shows the script the run recorded, not the path it came from', () => {
    render(
      <WorkflowRunDetail view={view({ script: "export const meta = { name: 'x' }" })} err="" onClose={noop} />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Script' }))
    expect(screen.getByText(/export const meta/)).toBeTruthy()
  })

  // Runs made before run records existed are exactly the ones an operator hunts
  // for; the tab has to say why it is empty rather than render a blank box.
  it('explains an absent script instead of showing nothing', () => {
    render(<WorkflowRunDetail view={view()} err="" onClose={noop} />)
    fireEvent.click(screen.getByRole('button', { name: 'Script' }))
    expect(screen.getByText(/predates run records/)).toBeTruthy()
  })

  it('collapses each result behind its label and size until asked', () => {
    render(
      <WorkflowRunDetail
        view={view({
          results: [{ label: 'bugs', agent_id: 'bugs-7f2a', bytes: 2048, result: { finding: 'one' } }],
        })}
        err=""
        onClose={noop}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /Results/ }))
    expect(screen.getByText('2.0 KB')).toBeTruthy()
    expect(screen.queryByText(/"finding"/)).toBeNull()
    fireEvent.click(screen.getByText('bugs'))
    expect(screen.getByText(/"finding"/)).toBeTruthy()
  })

  it('offers the resume command for a run worth resuming', () => {
    render(
      <WorkflowRunDetail
        view={view({ run: run({ status: 'incomplete', resumable: true, script_at: '/plans/review.js' }) })}
        err=""
        onClose={noop}
      />,
    )
    expect(screen.getByText('terva workflow run /plans/review.js --resume wf_0000000000aa')).toBeTruthy()
  })

  // A finished run has nothing to replay; offering the command would invite
  // paying for work that is already done.
  it('offers no resume command for a finished run', () => {
    render(<WorkflowRunDetail view={view()} err="" onClose={noop} />)
    expect(screen.queryByText(/--resume/)).toBeNull()
  })

  it('composes the resume command from the recorded launch path', () => {
    expect(resumeCommand('/plans/a.js', 'wf_1')).toBe('terva workflow run /plans/a.js --resume wf_1')
  })

  // Every copy here is a control on a surface with no hovered row above it, and
  // CopyButton's default variant is pinned into a message corner at opacity 0 —
  // so without `inline` all three rendered as nothing a user could see or reach.
  // Unit tests could not see it (happy-dom has no layout and applies no sheet);
  // a screenshot did. This pins the class the fix hangs on.
  it('asks for the in-flow copy variant, not the bubble one', () => {
    const { container } = render(
      <WorkflowRunDetail
        view={view({ script: 'export const meta = {}' })}
        err=""
        onClose={noop}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Script' }))
    expect(container.querySelector('.copy-btn.inline')).not.toBeNull()
  })
})

describe('pretty', () => {
  // A findings report is journaled as a JSON *string*. Rendering it through
  // JSON.stringify would print `"line one\nline two"` on one escaped line —
  // technically the value, and unreadable.
  it('unwraps a string result to the text the agent wrote', () => {
    expect(pretty('line one\nline two')).toBe('line one\nline two')
  })

  it('pretty-prints a structured deliverable', () => {
    expect(pretty({ a: 1 })).toBe('{\n  "a": 1\n}')
  })
})
