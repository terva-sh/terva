import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { ServerHello } from '../platform/ctrlproto/types'
import { appsFor, HELLO_WORDS } from './apps'

const hello = (h: Partial<ServerHello>): ServerHello => ({ role: 'server', ...h })
const ids = (h: ServerHello | null, current: Parameters<typeof appsFor>[1] = 'panel') => appsFor(h, current).map((a) => a.id)

// Each rule reads the hello, because a link to an app the daemon does not mount
// lands on a blank page. These pin which word in the hello turns each app on.
describe('appsFor', () => {
  it('lists only the panel for a daemon that serves nothing else', () => {
    expect(ids(hello({}))).toEqual(['panel'])
    // Before the hello lands there is nothing to go on, and the answer is the
    // same: no app is offered on a guess.
    expect(ids(null)).toEqual(['panel'])
  })

  it('lists Talkoot on the talkoot group and Stage on the stage feature', () => {
    expect(ids(hello({ groups: ['talkoot'] }))).toEqual(['panel', 'talkoot'])
    expect(ids(hello({ features: ['stage'] }))).toEqual(['panel', 'stage'])
    // A feature named like the group is not the group.
    expect(ids(hello({ features: ['talkoot'] }))).toEqual(['panel'])
  })

  it('hides Talkoot on a fleet hub, which grants the group and refuses its rooms', () => {
    // D4 in docs/proposals/web-hub.md, until TKT-01M4NVSKMS settles it.
    expect(ids(hello({ groups: ['talkoot'], features: ['fleet-hub'] }))).toEqual(['panel'])
  })

  it('lists the operator panel only on the supervisor feature', () => {
    // A group named tenants never reaches a tenant connection, so the group
    // cannot be the signal. The supervisor adds a feature instead (D2).
    expect(ids(hello({ features: ['operator-panel'] }))).toEqual(['panel', 'operator'])
    expect(ids(hello({ groups: ['tenants'] }))).toEqual(['panel'])
    const op = appsFor(hello({ features: ['operator-panel'] }), 'panel').find((a) => a.id === 'operator')
    expect(op?.href).toBe('/supervisor')
  })

  it('keeps the app on screen in the list, whatever the hello says', () => {
    // A person who opened /talkoot/ on a hub is in the Talkoot app. A switcher
    // that left it out would not name the app they are using.
    expect(ids(hello({ features: ['fleet-hub'], groups: ['talkoot'] }), 'talkoot')).toEqual(['panel', 'talkoot'])
    expect(ids(hello({}), 'stage')).toEqual(['panel', 'stage'])
  })

  it('links each app by the shared vocabulary in navlinks', () => {
    const all = appsFor(hello({ groups: ['talkoot'], features: ['stage', 'operator-panel'] }), 'panel')
    expect(Object.fromEntries(all.map((a) => [a.id, a.href]))).toEqual({
      panel: '/',
      talkoot: '/talkoot/',
      stage: '/stage/',
      operator: '/supervisor',
    })
  })
})

// The words above are typed out on the client side, and nothing types them
// against the daemon. A Go constant renamed or respelled would hide an app on
// every daemon while every test here kept passing, so read the source.
describe('the hello words appsFor reads', () => {
  it('match the constants in ctrlproto/hello.go', () => {
    const src = readFileSync(join(__dirname, '..', '..', '..', '..', 'ctrlproto', 'hello.go'), 'utf8')
    for (const [name, word] of Object.entries(HELLO_WORDS)) {
      const m = src.match(new RegExp(`\\b${name}\\s+(?:Group\\s+)?=\\s+"([^"]+)"`))
      expect(m, `ctrlproto/hello.go defines no ${name}`).not.toBeNull()
      expect(m![1], name).toBe(word)
    }
  })
})
