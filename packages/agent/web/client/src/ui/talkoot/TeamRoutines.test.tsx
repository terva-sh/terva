// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { TalkootRoutine } from '../../platform/ctrlproto/types'
import { TeamRoutines } from './TeamRoutines'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const next = new Date()
next.setHours(23, 5, 0, 0)

const task: TalkootRoutine = {
  id: '01A',
  member: 'helm',
  kind: 'task',
  schedule: 'every 15m',
  prompt: 'Check the CI run of each open pull request.',
  fields: { end_after_runs: 0, end_after_days: 14, spend_usd_per_day: 5, reviews_per_day: 2, follow_ups: 2, catch_up: true },
  status: 'active',
  approved_by: 'human:sothr',
  approved_at: next.toISOString(),
  next: next.toISOString(),
}

const spendKept = 'A routine that passes its daily spend limit pauses, and a run is stopped when its cost passes that limit.'
const reviewsCounted = 'Each review request in the chain of a routine counts against its review limit, and the request past the limit is refused.'
const reviewsNotCounted =
  'Nothing counts a review request, so the review limit binds nothing. The talkoot_routines.review_commands key names the commands that ask for a review.'

const noCard = 'While routines are on, a member asks for a routine, and writes to you, with no approval card.'

describe('the routines of a team', () => {
  it('shows nothing for a team with no routine policy and no routine', () => {
    const { container } = render(<TeamRoutines />)
    expect(container.innerHTML).toBe('')
  })

  it('says that routines are on, with the cap the person set', () => {
    render(<TeamRoutines routines={{ on: true, max: 4 }} />)
    expect(screen.getByText('Routines on: the team may hold 4')).toBeTruthy()
    // Routines on takes two approval cards from ask and auto-edit.
    expect(screen.getByText(noCard)).toBeTruthy()
    // With no routine, there is no limit to explain.
    expect(screen.queryByText(spendKept)).toBeNull()
    expect(screen.queryByText(reviewsNotCounted)).toBeNull()
  })

  it('says why a key that is set turns nothing on', () => {
    render(<TeamRoutines routines={{ on: false, problem: 'talkoot_routines.max is 0, and routines need 1 or more' }} />)
    expect(screen.queryByText(noCard)).toBeNull()
    expect(screen.getByText('Routines off')).toBeTruthy()
    expect(screen.getByText('talkoot_routines.max is 0, and routines need 1 or more')).toBeTruthy()
  })

  it('lists a routine with its kind, its member, its schedule, its next time, and every limit', () => {
    const { container } = render(<TeamRoutines routines={{ on: true, max: 4, routines: [task] }} />)
    const summary = container.querySelector('summary')!
    expect(summary.textContent).toContain('task')
    expect(summary.textContent).toContain('helm: every 15m')
    expect(summary.textContent).toContain('next 23:05')
    expect(screen.getByText('Check the CI run of each open pull request.')).toBeTruthy()
    const rows = [...container.querySelectorAll('.talkoot-fields tr')].map((tr) => tr.textContent)
    expect(rows).toEqual([
      'Spend in one day$5.00',
      'Review requests in one day2',
      'Follow-ups after one run2',
      'After a missed timeone late run',
      'Ends after this many runsno end',
      'Ends after this many days14',
    ])
    // 🚨 The list shows a spend limit, so it must say what stops a run.
    expect(screen.getByText(spendKept)).toBeTruthy()
    // 🚨 It shows a review limit too. With no command to count, the limit
    // binds nothing, and the list must not let it read as a limit.
    expect(screen.getByText(reviewsNotCounted).className).toContain('authority')
    expect(screen.queryByText(reviewsCounted)).toBeNull()
    expect(screen.queryByText(/Reviews asked for today/)).toBeNull()
    expect(screen.getByText('Runs started: 0')).toBeTruthy()
    // A viewer gets no button.
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows the text that the kind takes, and an amount below a cent in full', () => {
    const odd: TalkootRoutine = { ...task, command: '   ', fields: { ...task.fields, spend_usd_per_day: 5.004 } }
    const { container } = render(<TeamRoutines routines={{ on: true, max: 4, routines: [odd] }} />)
    expect(container.querySelector('pre')!.textContent).toBe('Check the CI run of each open pull request.')
    expect(container.querySelector('.talkoot-fields tr')!.textContent).toBe('Spend in one day$5.004')
  })

  it('pauses an active routine and resumes a paused one', () => {
    const onSet = vi.fn()
    const paused: TalkootRoutine = { ...task, id: '01B', member: 'jev', status: 'paused', next: undefined, changed_by: 'human:sothr', reason: 'Too noisy.' }
    render(<TeamRoutines routines={{ on: true, max: 4, routines: [task, paused] }} onSet={onSet} />)
    fireEvent.click(screen.getByLabelText('Pause the routine of helm'))
    fireEvent.click(screen.getByLabelText('Resume the routine of jev'))
    expect(onSet.mock.calls).toEqual([
      ['01A', 'pause'],
      ['01B', 'resume'],
    ])
    expect(screen.getByText('paused')).toBeTruthy()
    expect(screen.getByText('Paused by sothr: Too noisy.')).toBeTruthy()
    expect(screen.queryByLabelText('Pause the routine of jev')).toBeNull()
  })

  it('asks before it deletes a routine, and deletes nothing on a no', () => {
    const onSet = vi.fn()
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    render(<TeamRoutines routines={{ on: true, max: 4, routines: [task] }} onSet={onSet} />)
    fireEvent.click(screen.getByLabelText('Delete the routine of helm'))
    expect(confirm).toHaveBeenCalledTimes(1)
    expect(onSet).not.toHaveBeenCalled()
    confirm.mockReturnValue(true)
    fireEvent.click(screen.getByLabelText('Delete the routine of helm'))
    expect(onSet.mock.calls).toEqual([['01A', 'delete']])
  })

  it('shows why a routine does not run at its time', () => {
    render(<TeamRoutines routines={{ on: true, max: 4, routines: [{ ...task, problem: 'A watcher does not run yet.' }] }} />)
    expect(screen.getByText('A watcher does not run yet.')).toBeTruthy()
  })

  it('shows what a routine spent today against its limit, and no spend for a watcher', () => {
    render(<TeamRoutines routines={{ on: true, max: 4, routines: [{ ...task, spent_today_usd: 1.25 }] }} />)
    expect(screen.getByText('Spent today: $1.25 of $5.00')).toBeTruthy()
    cleanup()
    render(<TeamRoutines routines={{ on: true, max: 4, routines: [{ ...task, kind: 'watcher', prompt: undefined, command: 'just ci' }] }} />)
    expect(screen.queryByText(/Spent today/)).toBeNull()
  })

  it('shows the reviews that a routine asked for today while the daemon counts them', () => {
    render(<TeamRoutines routines={{ on: true, max: 4, reviews_counted: true, routines: [{ ...task, reviews_today: 1 }] }} />)
    expect(screen.getByText(reviewsCounted).className).not.toContain('authority')
    expect(screen.queryByText(reviewsNotCounted)).toBeNull()
    expect(screen.getByText('Reviews asked for today: 1 of 2')).toBeTruthy()
    cleanup()
    // No count on record reads as no request, against the routine's limit.
    render(<TeamRoutines routines={{ on: true, max: 4, reviews_counted: true, routines: [task] }} />)
    expect(screen.getByText('Reviews asked for today: 0 of 2')).toBeTruthy()
    cleanup()
    // A watcher runs a command and no model, so it asks for no review.
    render(<TeamRoutines routines={{ on: true, max: 4, reviews_counted: true, routines: [{ ...task, kind: 'watcher', prompt: undefined, command: 'just ci' }] }} />)
    expect(screen.queryByText(/Reviews asked for today/)).toBeNull()
  })

  it('counts the runs, and says when a run is at work', () => {
    const { container } = render(<TeamRoutines routines={{ on: true, max: 4, routines: [{ ...task, runs: 3, run_session: 's1' }] }} />)
    expect(screen.getByText('Runs started: 3')).toBeTruthy()
    // No spend on record reads as nothing spent, against the routine's limit.
    expect(screen.getByText('Spent today: $0.00 of $5.00')).toBeTruthy()
    // The run at work stands in the place of the next time.
    expect(container.querySelector('summary')!.textContent).toContain('a run is at work')
    expect(container.querySelector('summary')!.textContent).not.toContain('next')
  })

  it('shows the cause of a pause that no person made', () => {
    const onSet = vi.fn()
    const failed: TalkootRoutine = { ...task, status: 'paused', next: undefined, reason: 'a run failed: refused' }
    render(<TeamRoutines routines={{ on: true, max: 4, routines: [failed] }} onSet={onSet} />)
    // Outside the details, so a person sees it without a click.
    expect(screen.getByText('Paused: a run failed: refused').closest('details')).toBeNull()
    expect(screen.getByLabelText('Resume the routine of helm')).toBeTruthy()
  })

  it('shows an ended routine with its cause, and offers only its delete', () => {
    const onSet = vi.fn()
    const ended: TalkootRoutine = { ...task, status: 'ended', next: undefined, runs: 5, reason: 'it started 5 of its 5 runs' }
    const { container } = render(<TeamRoutines routines={{ on: true, max: 4, routines: [ended] }} onSet={onSet} />)
    expect(container.querySelector('summary')!.textContent).toContain('ended')
    expect(screen.getByText('Ended: it started 5 of its 5 runs')).toBeTruthy()
    // 🚨 An ended routine does not pause and does not resume.
    expect(screen.queryByLabelText('Pause the routine of helm')).toBeNull()
    expect(screen.queryByLabelText('Resume the routine of helm')).toBeNull()
    expect(screen.getByLabelText('Delete the routine of helm')).toBeTruthy()
  })

  it('names a record that does not read, and offers only its delete', () => {
    const onSet = vi.fn()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const damaged = { id: '01C', problem: 'the record of the routine does not read; delete it to clear it' } as TalkootRoutine
    render(<TeamRoutines routines={{ on: true, max: 4, routines: [damaged] }} onSet={onSet} />)
    expect(screen.getByText('the record of the routine does not read; delete it to clear it')).toBeTruthy()
    expect(screen.getAllByRole('button')).toHaveLength(1)
    fireEvent.click(screen.getByLabelText('Delete the routine'))
    expect(onSet.mock.calls).toEqual([['01C', 'delete']])
  })

  it('says that a routine does not resume while routines are off', () => {
    render(<TeamRoutines routines={{ on: false, routines: [{ ...task, status: 'paused', next: undefined }] }} />)
    expect(screen.getByText('A routine does not resume while routines are off.')).toBeTruthy()
  })
})
