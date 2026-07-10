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
