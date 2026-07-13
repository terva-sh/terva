import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// Flow 6: NO pane surface scrolls horizontally, at any width the pane actually
// renders at (styles.css conventions #1–#3). The settings and raati panes both
// shipped as two-axis scrollers on phones because a <select> never shrinks
// below its widest option on its own — and surface content is server-driven,
// so the client cannot control its width. Chromium honors a max-width cap on
// the box; WebKit additionally keeps counting the untruncated option text as
// scrollable overflow (the settings pane panned sideways with nothing visibly
// cut), which is why this file also runs under the webkit-mobile project.
//
// The sweep renders every core surface with realistic worst-width data (the
// settings enums mirror workspace/workspace_settings.go; the raati form is
// app.tsx's) at each width tier: phone sheet, tablet portrait, tablet
// landscape, and the desktop rail.

const VIEWPORTS = [
  { name: 'phone', width: 390, height: 780 },
  { name: 'tablet-portrait', width: 744, height: 1000 },
  { name: 'tablet-landscape', width: 1024, height: 780 },
  { name: 'desktop', width: 1280, height: 800 },
]

const MODELS = [
  { id: 'claude-opus-4-8', provider: 'anthropic', current: true },
  { id: 'claude-haiku-4-5-20251001', provider: 'anthropic' },
  { id: 'gpt-5.6-sol', provider: 'openai-codex' },
]

const SETTINGS_VIEW = {
  items: [
    {
      key: 'approval', label: 'Approval mode', type: 'enum', value: 'workspace',
      options: [
        { value: 'auto-edit', label: 'auto-edit — auto reads/edits, ask commands' },
        { value: 'workspace', label: 'workspace — auto in-workspace, ask foreign' },
      ],
      description: 'How tool calls are gated for this session.',
      note: 'per-session — not saved (a security posture, like the TUI)',
    },
    {
      key: 'reasoning', label: 'Thinking', type: 'enum', value: 'max',
      options: [
        { value: 'maximum', label: 'maximum — highest (~32k tokens)' },
        { value: 'max', label: 'max — native max (GPT-5.6 / adaptive Claude)' },
      ],
      description: 'Reasoning effort. Applies live and becomes the default for new sessions.',
    },
    {
      key: 'auto_compact', label: 'Auto-condense', type: 'enum', value: 'steps',
      options: [
        { value: 'steps', label: 'steps — condense mid-turn as the window fills' },
        { value: 'turns', label: 'turns — condense only at turn boundaries' },
      ],
      description: 'When to automatically condense the transcript as the context window fills.',
      note: 'applies live to every session',
    },
    { key: 'lazy_tools', label: 'Lazy tool loading', type: 'bool', value: 'false', description: 'Advertise only the core coding tools at first and let the agent load extension/MCP tool groups on demand (activate_tools), trimming the tool schemas that fill context every turn.', note: 'applies to new sessions' },
  ],
}

const RAATI_VIEW = {
  running: false,
  profiles: [{ name: 'release-gate', description: 'gate + cross-provider käräjät for release decisions' }],
  history: [
    {
      id: 'r1', when: '2026-07-11T10:00:00Z',
      question: 'Should we merge the reconnect teardown fix before the release cut, or hold it for the next minor?',
      class: 'gate', decision: 'approved',
      tally: { approve: 3, reject: 0, abstain: 0, absent: 0 },
    },
  ],
}

const TASKS_VIEW = {
  tasks: [
    {
      id: 'task-1', status: 'running', model: 'claude-haiku-4-5-20251001', provider: 'anthropic',
      task: 'Sweep every call site of the deprecated resolver and rewrite it against the new CredentialSource seam, keeping behavior identical',
      activity: 'editing packages/provider/anthropic/client_credentials_test.go',
      tail: 'ok  \tterva.sh/terva/packages/provider\t2.117s\n--- building packages/agent (terva_web,terva_acp) --- unbroken-token-'.padEnd(220, 'x'),
    },
    { id: 'task-2', status: 'failed', task: 'Port the swarm-spawn staleness fix', error: 'exit 1: packages/agent/swarm_spawn.go:88: undefined: staleCutoff' },
  ],
}

// A deliberately wide table: the .wg-table-wrap inner scroller must absorb it
// (convention #2) without the pane itself growing a horizontal axis.
const WIDGETS_VIEW = [
  { type: 'heading', text: 'Context breakdown', level: 2 },
  {
    type: 'table',
    columns: ['file', 'tokens', 'share', 'last touched by'],
    cells: [
      ['packages/agent/workspace/workspace_settings_extremely_long_path_name.go', '18,442', '12.4%', 'session_0119gr6zgmGoSAHvLbdH43yK'],
      ['packages/agent/web/client/src/app.tsx', '9,120', '6.1%', 'session_0119gr6zgmGoSAHvLbdH43yK'],
    ],
  },
  { type: 'keyvalue', rows: [{ key: 'window', value: '1,050,000 tokens', note: 'DesiredContextWindow', mono: true }] },
  { type: 'meter', label: 'context', value: 148000, max: 1050000, unit: 'tok' },
]

const CHAT_VIEW = {
  services: [{ name: 'telegram', configured: true, paired: true }],
  bridge: { state: 'connected', connector: 'telegram', username: 'terva_dev_bot', paired_id: '@drew', session: 'sess-0119gr6zgm' },
}

// Every core surface the daemon serves, with a per-surface readiness selector
// (the body element that proves the view rendered, not the loading stub).
const SURFACES: { id: string; title: string; kind: string; ready: string; view: Record<string, unknown> }[] = [
  { id: 'tasks', title: 'Tasks', kind: 'tasks', ready: '.tasks-body', view: { tasks: TASKS_VIEW } },
  { id: 'raati', title: 'Raati', kind: 'raati', ready: '.raati-body', view: { raati: RAATI_VIEW } },
  { id: 'settings', title: 'Settings', kind: 'settings', ready: '.settings-body', view: { settings: SETTINGS_VIEW } },
  { id: 'widgets', title: 'Usage', kind: 'widgets', ready: '.wg-body', view: { widgets: WIDGETS_VIEW } },
  {
    id: 'commands', title: 'Commands', kind: 'commands', ready: '.cmd-body',
    view: { commands: { commands: [{ ext: 'terva-index', name: 'reindex-workspace-search', description: 'Rebuild the workspace search index from scratch, honoring .gitignore' }] } },
  },
  {
    id: 'extensions', title: 'Extensions', kind: 'extensions', ready: '.ext-body',
    view: { extensions: { extensions: [{ name: 'terva-memory', version: '0.4.2', language: 'go', description: 'Two-tier searchable memory with decay and a dreaming refiner', scope: 'user', status: 'running', enabled: true, tools: 3, commands: 1 }] } },
  },
  {
    id: 'permissions', title: 'Permissions', kind: 'permissions', ready: '.perm-body',
    view: { permissions: { mode: 'workspace', rules: [{ tool: 'Bash', args: 'git push --force-with-lease origin *', decision: 'ask', source: 'project settings', removable: true }], grants: ['Bash(npm --prefix packages/agent/web/client run build)'] } },
  },
  {
    id: 'lore', title: 'Lore', kind: 'lore', ready: '.lore-body',
    view: { lore: { entries: [{ name: 'release-gotchas', keys: ['release', 'cut', 'goreleaser'], content: 'Push the release branch first, wait for the GitHub windows-latest test job to go green, then push the tag.', source: 'user/lore/release.md', editable: true, scope: 'user' }], can_project: true } },
  },
  {
    id: 'mcp', title: 'MCP', kind: 'mcp', ready: '.ext-body',
    view: { mcp: { servers: [{ name: 'github', scope: 'user', description: 'GitHub issues, PRs and code search over the REST API', status: 'running', enabled: true, tools: 12 }] } },
  },
  { id: 'chat', title: 'Chat', kind: 'chat', ready: '.kv-card', view: { chat: CHAT_VIEW } },
]

const respond = (method: string, params: unknown) => {
  if (method === 'models.list') return { models: MODELS }
  if (method === 'surfaces.list') return { surfaces: SURFACES.map(({ id, title, kind }) => ({ id, title, kind })) }
  if (method === 'surface.get') {
    const id = (params as { id?: string } | null)?.id
    const s = SURFACES.find((x) => x.id === id) ?? SURFACES[0]
    return { surface: { id: s.id, title: s.title, kind: s.kind, ...s.view } }
  }
  return undefined
}

for (const surface of SURFACES) {
  test(`${surface.title} pane stays one-axis at every width`, async ({ page }) => {
    await page.setViewportSize({ width: VIEWPORTS[0].width, height: VIEWPORTS[0].height })
    await installMockBackend(page, { respond })
    await page.goto('/')
    await page.locator('.topbar .dot.open').waitFor()

    await page.locator('.topbar button[title="Panes (usage, settings, extensions)"]').click()
    await page.locator(`.pane-tab[title="${surface.title}"]`).click()
    await page.locator(surface.ready).first().waitFor()

    for (const vp of VIEWPORTS) {
      await page.setViewportSize({ width: vp.width, height: vp.height })

      // No element may extend past the viewport edges — except inside an
      // inner scroller (convention #2: .wg-table-wrap etc. exist precisely to
      // hold wider-than-pane content; the scroller's own box is still checked).
      const spill = await page.evaluate(() => {
        const vw = document.documentElement.clientWidth
        const pane = document.querySelector('.pane-body')!
        const inInnerScroller = (el: Element) => {
          for (let a = el.parentElement; a && a !== pane; a = a.parentElement) {
            const ox = getComputedStyle(a).overflowX
            if (ox === 'auto' || ox === 'scroll') return true
          }
          return false
        }
        const out: string[] = []
        document.querySelectorAll('.pane-body, .pane-body *').forEach((el) => {
          const r = el.getBoundingClientRect()
          if ((r.right > vw + 0.5 || r.left < -0.5) && !inInnerScroller(el))
            out.push(`${el.tagName}.${[...el.classList].join('.')} [${r.left},${r.right}]`)
        })
        return out
      })
      expect(spill, `${vp.name} (${vp.width}px)`).toEqual([])

      // …and the pane must have zero horizontal scroll range: a forced
      // scrollLeft clamps straight back to 0 (this is what caught WebKit's
      // phantom option-text overflow, which spills no visible pixels).
      const pan = await page.evaluate(() => {
        const el = document.querySelector<HTMLElement>('.pane-body')!
        el.scrollLeft = 120
        return el.scrollLeft
      })
      expect(pan, `${vp.name} (${vp.width}px)`).toBe(0)

      // The shell must not scroll horizontally either — the topbar wraps its
      // control row on narrow screens instead of spilling past the viewport.
      const docPan = await page.evaluate(() => {
        const doc = document.scrollingElement as HTMLElement
        doc.scrollLeft = 120
        return doc.scrollLeft
      })
      expect(docPan, `${vp.name} (${vp.width}px) document`).toBe(0)
    }
  })
}
