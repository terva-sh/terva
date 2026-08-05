import { test, expect } from '@playwright/test'
import { installMockBackend } from './support'

// The reasoning picker is a SHARED component (ui/ReasoningPick.tsx, styled in
// ui/ui.css) that both apps render. Its rules were written against the panel's
// private palette — --panel, --line, --muted, --fg — and Stage does not import
// styles.css at all, so under Stage every one of them resolved to nothing: the
// popup painted with no background and no border, its rows floating unreadably
// over the transcript.
//
// ui-conformance.test.ts now forbids the cause (a shared sheet reading an
// app-private token) and runs in CI, which is the durable guard. This is the
// other half, and the half a source-read cannot give: the browser resolving the
// real cascade and reporting what the element is actually painted.
//
// It needs no session — the question is entirely about the stylesheet, so the
// element is mounted directly and measured.
const surfaceOf = () => {
  const el = document.createElement('div')
  el.className = 'reasoning-pick'
  document.body.appendChild(el)
  const cs = getComputedStyle(el)
  const out = { background: cs.backgroundColor, border: cs.borderTopColor, width: cs.borderTopWidth }
  el.remove()
  return out
}

// A colour the eye cannot see through. Playwright reports computed colours as
// rgb()/rgba(); only the 4-arg form can carry transparency, and an unresolved
// var() leaves the property at its initial value, which is transparent.
function opaque(colour: string): boolean {
  const m = colour.match(/rgba?\(([^)]+)\)/)
  if (!m) return false
  const parts = m[1].split(',').map((s) => parseFloat(s.trim()))
  const alpha = parts.length > 3 ? parts[3] : 1
  return alpha > 0.9
}

for (const [app, path] of [
  ['stage', '/stage.html'],
  ['panel', '/'],
] as const) {
  test(`${app}: the reasoning picker paints a surface, not the page behind it`, async ({
    page,
  }) => {
    await installMockBackend(page, {
      respond: (method) => {
        if (method === 'cards.list') return { cards: [] }
        if (method === 'personas.list') return { personas: [] }
        return undefined
      },
    })
    await page.goto(path)

    const got = await page.evaluate(surfaceOf)
    expect(
      opaque(got.background),
      `${app}: .reasoning-pick background computed to ${got.background} — the ` +
        `transcript shows straight through it. A --ui-* token this app does not ` +
        `map leaves the property at its transparent initial value.`,
    ).toBe(true)
    // The border is the other half of reading as a raised surface, and it failed
    // the same way for the same reason.
    expect(
      opaque(got.border),
      `${app}: .reasoning-pick border computed to ${got.border} at width ${got.width}`,
    ).toBe(true)
  })
}
