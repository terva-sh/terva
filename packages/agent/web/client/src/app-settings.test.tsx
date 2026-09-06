// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render } from '@testing-library/preact'

import { SettingsBody, settingSections } from './app'
import type { SettingItem, SettingsView } from './platform/ctrlproto/types'

// The settings pane renders one section per wire group.
//
// The compatibility rule cuts both ways and is the reason most of these exist.
// Items stay one flat array ordered group by group, so a client that ignores
// groups renders what it always did; and a client that knows groups must not
// lose an item whose group it cannot place. A dropped row is invisible, which
// is exactly the failure a settings pane cannot afford.

const item = (key: string, group?: string): SettingItem => ({
  key,
  label: `Label ${key}`,
  type: 'bool',
  value: 'false',
  description: `What ${key} does.`,
  ...(group ? { group } : {}),
})

const view = (over: Partial<SettingsView> = {}): SettingsView => ({
  groups: [
    { id: 'security', label: 'Security & trust', description: 'who may run what' },
    { id: 'context', label: 'Context & prompt' },
  ],
  items: [item('approval', 'security'), item('trust', 'security'), item('lazy_tools', 'context')],
  ...over,
})

afterEach(cleanup)

describe('settingSections', () => {
  it('follows the order the daemon declared, not the order items happen to arrive in', () => {
    const secs = settingSections(view())
    expect(secs.map((s) => s.id)).toEqual(['security', 'context'])
    expect(secs[0].items.map((i) => i.key)).toEqual(['approval', 'trust'])
    expect(secs[0].description).toBe('who may run what')
  })

  // The failure this bucket exists for: someone adds a setting and forgets to
  // assign it a group. It must show up somewhere a person can see.
  it('collects an undeclared or absent group into a trailing labelled bucket', () => {
    const secs = settingSections(
      view({ items: [item('approval', 'security'), item('orphan', 'nonexistent'), item('nameless')] }),
    )
    expect(secs.map((s) => s.id)).toEqual(['security', ''])
    const bucket = secs[secs.length - 1]
    expect(bucket.label).toBeTruthy()
    expect(bucket.items.map((i) => i.key)).toEqual(['orphan', 'nameless'])
  })

  // A daemon that predates groups sends none. One header reading "other" over
  // the whole pane would be worse than no header at all.
  it('renders a groupless view as one bare section', () => {
    const secs = settingSections({ items: [item('approval'), item('trust')] })
    expect(secs).toHaveLength(1)
    expect(secs[0].label).toBeUndefined()
    expect(secs[0].items).toHaveLength(2)
  })

  // Conditional items mean a declared group can be empty this session.
  it('drops a declared group that has no items', () => {
    const secs = settingSections(view({ items: [item('lazy_tools', 'context')] }))
    expect(secs.map((s) => s.id)).toEqual(['context'])
  })

  it('keeps every item exactly once', () => {
    const v = view({ items: [item('approval', 'security'), item('orphan', 'nope'), item('lazy_tools', 'context')] })
    const seen = settingSections(v).flatMap((s) => s.items.map((i) => i.key))
    expect(seen.sort()).toEqual(['approval', 'lazy_tools', 'orphan'])
  })
})

describe('SettingsBody', () => {
  it('gives each group a heading and puts its rows under it', () => {
    const { container } = render(<SettingsBody v={view()} onAction={vi.fn()} />)

    const heads = [...container.querySelectorAll('.set-group-label')].map((e) => e.textContent)
    expect(heads).toEqual(['Security & trust', 'Context & prompt'])

    const groups = container.querySelectorAll('.set-group')
    expect(groups).toHaveLength(2)
    expect(groups[0].querySelectorAll('.set-row')).toHaveLength(2)
    expect(groups[1].querySelectorAll('.set-row')).toHaveLength(1)
  })

  // The TUI elides an unfocused description because it pays for it in terminal
  // rows. A scrolling pane does not, so the web keeps them all.
  it('keeps the full description on every row', () => {
    const { container } = render(<SettingsBody v={view()} onAction={vi.fn()} />)
    const descs = [...container.querySelectorAll('.set-desc')].map((e) => e.textContent)
    expect(descs).toEqual(['What approval does.', 'What trust does.', 'What lazy_tools does.'])
  })

  it('draws no heading when the view carries no groups', () => {
    const { container } = render(<SettingsBody v={{ items: [item('approval')] }} onAction={vi.fn()} />)
    expect(container.querySelectorAll('.set-group-head')).toHaveLength(0)
    expect(container.querySelectorAll('.set-row')).toHaveLength(1)
  })

  // Grouping changed where a row is drawn, not what clicking it sends.
  it('still fires settings/set for the row that was toggled', () => {
    const onAction = vi.fn()
    const { container } = render(<SettingsBody v={view()} onAction={onAction} />)
    const toggles = container.querySelectorAll<HTMLButtonElement>('.set-toggle')
    toggles[toggles.length - 1].click()
    expect(onAction).toHaveBeenCalledWith('settings', 'set', { key: 'lazy_tools', value: 'true' })
  })
})
