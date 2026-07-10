import MarkdownIt from 'markdown-it'
import { t } from './i18n'

// html:false makes markdown-it ESCAPE any raw HTML in the model's output (so a
// stray <script> renders as text, not markup) and it validates link URLs
// (javascript: is rejected) — the accepted XSS-safe config without a separate
// sanitizer. Tables, headings, links, lists, and code blocks are all in the
// default preset; syntax highlighting is intentionally not wired yet.
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: false,
})

// Render links target=_blank with rel=noopener so a model-provided link can't
// reach back into the panel via window.opener.
const defaultLinkOpen =
  md.renderer.rules.link_open ||
  ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))
md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
  tokens[idx].attrSet('target', '_blank')
  tokens[idx].attrSet('rel', 'noopener noreferrer')
  return defaultLinkOpen(tokens, idx, options, env, self)
}

// Wrap each rendered code block in a .code-wrap carrying a .code-copy button.
// The button has no per-block handler — a delegated click listener on the
// transcript (app.tsx) reads the block's text at click time and copies it, so
// the raw HTML stays inert. Both icons ship inline; CSS shows one via .copied.
const copyIcons =
  '<svg class="ic ic-copy" viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>' +
  '<svg class="ic ic-check" viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>'

// escapeAttr HTML-escapes a string for interpolation into a double-quoted
// attribute inside a raw HTML string. Locale overlays are user-supplied
// content ($TERVA_HOME/locales), so t() output is NOT trusted in this sink —
// an unescaped quote in a translation would break out of the attribute
// right next to model-rendered HTML.
export function escapeAttr(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function withCopy(html: string): string {
  const label = escapeAttr(t('Copy'))
  return `<div class="code-wrap"><button class="code-copy" type="button" aria-label="${label}" title="${label}">${copyIcons}</button>${html}</div>`
}

for (const rule of ['fence', 'code_block'] as const) {
  const base = md.renderer.rules[rule]
  md.renderer.rules[rule] = (tokens, idx, options, env, self) =>
    withCopy(base ? base(tokens, idx, options, env, self) : self.renderToken(tokens, idx, options))
}

export function renderMarkdown(src: string): string {
  return md.render(src)
}
