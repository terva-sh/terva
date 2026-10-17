// The apps a daemon serves, as the app switcher and the hub list them.
//
// Each entry is read from the hello the app already holds, never from a guess:
// a link to an app the daemon does not mount falls through to the root page,
// whose relative assets then fail, and the person sees a blank screen. The
// hello is what the server says it serves, so it is the only safe source.
//
// docs/proposals/web-hub.md holds the decisions behind each rule (D2 and D4).

import { t } from '../i18n'
import type { ServerHello } from '../platform/ctrlproto/types'
import { panelHref, stageHref, talkootHref } from './navlinks'

export type AppId = 'panel' | 'talkoot' | 'stage' | 'operator'

export interface AppEntry {
  id: AppId
  href: string
  // A glyph that marks the app wherever it is listed. Decorative: the name
  // carries the meaning, so the mark is hidden from assistive technology.
  mark: string
  name: string
  // One line that says what the app is for.
  blurb: string
}

// The hello words the rules read, keyed by the Go constant each one mirrors in
// ctrlproto/hello.go. apps.test.ts reads that file and holds the two in step.
export const HELLO_WORDS = {
  FeatureStage: 'stage',
  FeatureFleetHub: 'fleet-hub',
  FeatureOperatorPanel: 'operator-panel',
  GroupTalkoot: 'talkoot',
} as const
const FEATURE_STAGE = HELLO_WORDS.FeatureStage
const FEATURE_FLEET_HUB = HELLO_WORDS.FeatureFleetHub
const FEATURE_OPERATOR_PANEL = HELLO_WORDS.FeatureOperatorPanel
const GROUP_TALKOOT = HELLO_WORDS.GroupTalkoot

/**
 * appsFor lists the apps this daemon serves, in switcher order.
 *
 * `current` is always in the list, whatever the hello says. A person who
 * reached an app by its address is in it, and a switcher that left out the app
 * on screen would read as broken.
 */
export function appsFor(hello: ServerHello | null | undefined, current: AppId): AppEntry[] {
  const features = hello?.features ?? []
  const groups = hello?.groups ?? []
  const has = (id: AppId): boolean => {
    switch (id) {
      case 'panel':
        return true
      case 'talkoot':
        // A fleet hub grants the talkoot group and then refuses every room
        // subscription (fleet/aggregate.go), so it is hidden there until
        // TKT-01M4NVSKMS settles what a hub serves.
        return groups.includes(GROUP_TALKOOT) && !features.includes(FEATURE_FLEET_HUB)
      case 'stage':
        return features.includes(FEATURE_STAGE)
      case 'operator':
        // Added by the supervisor under `terva serve`, and only for a person
        // whose grant includes the tenants group (tenant.NarrowHello).
        return features.includes(FEATURE_OPERATOR_PANEL)
    }
  }
  const all: AppEntry[] = [
    { id: 'panel', href: panelHref(), mark: '⌂', name: t('Panel'), blurb: t('Sessions, settings, and the workspace') },
    { id: 'talkoot', href: talkootHref(), mark: '⁂', name: t('Talkoot'), blurb: t('Watch and steer a team of agents') },
    { id: 'stage', href: stageHref(), mark: '🎭', name: t('Stage'), blurb: t('Characters, worlds, and scenes') },
    {
      id: 'operator',
      href: '/supervisor',
      mark: '⚒',
      name: t('Operator panel'),
      blurb: t('Environments on this host, and who may reach them'),
    },
  ]
  return all.filter((a) => a.id === current || has(a.id))
}
