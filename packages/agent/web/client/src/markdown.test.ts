import { describe, it, expect, afterEach } from 'vitest'
import { escapeAttr, renderMarkdown } from './markdown'
import { applyServerCatalog, setLocale } from './i18n'

// The copy-button wrapper interpolates t('Copy') into attribute positions
// inside a raw-HTML string that reaches dangerouslySetInnerHTML. Locale
// overlays ($TERVA_HOME/locales/web) are operator/user-supplied content, so
// a hostile translation must never break out of the attribute.

afterEach(() => setLocale('en')) // drop any overlay the test applied

describe('escapeAttr', () => {
  it('escapes every attribute-breaking character', () => {
    expect(escapeAttr(`"><script>alert(1)</script>`)).toBe(
      '&quot;&gt;&lt;script&gt;alert(1)&lt;/script&gt;',
    )
    expect(escapeAttr(`a & b < c > d " e ' f`)).toBe(
      'a &amp; b &lt; c &gt; d &quot; e &#39; f',
    )
    expect(escapeAttr('plain Copy')).toBe('plain Copy')
  })
})

describe('renderMarkdown copy button', () => {
  it('keeps a hostile translation inert inside the attribute', () => {
    // Exactly what a user locale overlay does at runtime.
    applyServerCatalog({ lang: 'en', singular: { Copy: `"><img src=x onerror=alert(1)>` } })
    const html = renderMarkdown('```go\ncode\n```')
    expect(html).not.toContain('<img src=x')
    expect(html).toContain('&quot;&gt;&lt;img')
    // The button structure survives intact.
    expect(html).toContain('<button class="code-copy"')
  })

  it('renders the normal label untouched', () => {
    const html = renderMarkdown('```\nx\n```')
    expect(html).toContain('aria-label="Copy"')
  })
})

// The security posture markdown.ts documents (html:false, safe link URLs, and
// target/rel on links) is the difference between rendering model output and
// executing it. Pin each invariant so a config flip can't slip through.
describe('renderMarkdown safety config', () => {
  it('escapes raw HTML from model output instead of emitting live markup', () => {
    const html = renderMarkdown('<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>')
    expect(html).not.toContain('<script>')
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;script&gt;')
    expect(html).toContain('&lt;img')
  })

  it('opens links in a new tab with rel=noopener noreferrer', () => {
    const html = renderMarkdown('[docs](https://example.com)')
    expect(html).toContain('target="_blank"')
    expect(html).toContain('rel="noopener noreferrer"')
  })

  it('never emits a javascript: href', () => {
    const html = renderMarkdown('[x](javascript:alert(1))')
    expect(html).not.toContain('href="javascript')
  })
})
