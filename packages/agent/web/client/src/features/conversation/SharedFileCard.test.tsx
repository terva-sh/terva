// @vitest-environment happy-dom
import { cleanup, render } from '@testing-library/preact'
import { afterEach, describe, expect, it } from 'vitest'
import type { SharedFile } from '../../platform/ctrlproto/types'
import { SharedFileCard } from './SharedFileCard'

const file = (over: Partial<SharedFile> = {}): SharedFile => ({
  id: 'shr_abc',
  name: 'report.pdf',
  kind: 'document',
  size: 2048,
  ...over,
})

const card = (f: SharedFile, sess = 'ses_1', canDownload = true) =>
  render(<SharedFileCard file={f} sess={sess} canDownload={canDownload} />).container

afterEach(cleanup)

describe('SharedFileCard', () => {
  it('offers a download scoped to the session', () => {
    const link = card(file()).querySelector('a.shared-file__row') as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/shared/ses_1/shr_abc')
    // The download attribute is what makes the browser keep the agent's name
    // for the file rather than inventing one from the URL's last segment.
    expect(link.getAttribute('download')).toBe('report.pdf')
    expect(link.textContent).toContain('report.pdf')
    expect(link.textContent).toContain('2.0 KB')
  })

  // The id and the session both ride a URL path, so both are escaped. Neither
  // can contain a separator by the time it reaches here (the store sanitizes),
  // but the encoding is what makes that a belt rather than an assumption.
  it('escapes what it puts in the path', () => {
    const link = card(file({ id: 'shr_a b' }), 'ses/../1').querySelector('a.shared-file__row')
    expect(link?.getAttribute('href')).toBe('/shared/ses%2F..%2F1/shr_a%20b')
  })

  it('renders an image inline and still offers the download', () => {
    const el = card(file({ name: 'chart.png', kind: 'image' }))
    const img = el.querySelector('img') as HTMLImageElement
    // ?inline=1 is the opt-in the daemon honours only for safe media types.
    expect(img.getAttribute('src')).toBe('/shared/ses_1/shr_abc?inline=1')
    expect(img.getAttribute('alt')).toBe('chart.png')
    expect(el.querySelector('a.shared-file__row')?.getAttribute('href')).toBe('/shared/ses_1/shr_abc')
  })

  it('gives audio and video real players', () => {
    const audio = card(file({ name: 'clip.mp3', kind: 'audio' })).querySelector('audio')
    expect(audio?.getAttribute('src')).toBe('/shared/ses_1/shr_abc?inline=1')
    expect(audio?.hasAttribute('controls')).toBe(true)

    const video = card(file({ name: 'clip.mp4', kind: 'video' })).querySelector('video')
    expect(video?.getAttribute('src')).toBe('/shared/ses_1/shr_abc?inline=1')
    expect(video?.hasAttribute('controls')).toBe(true)
  })

  it('renders no player for a document', () => {
    const el = card(file())
    expect(el.querySelector('img')).toBeNull()
    expect(el.querySelector('audio')).toBeNull()
    expect(el.querySelector('video')).toBeNull()
  })

  it('shows a caption when there is one', () => {
    expect(card(file({ caption: 'week 32 latency' })).textContent).toContain('week 32 latency')
    expect(card(file()).querySelector('.shared-file__caption')).toBeNull()
  })

  // No route behind it: say what was shared, offer nothing, and — the part that
  // matters — do not render an <img> whose src would 404 into a broken icon.
  it('degrades to an inert label without a download route', () => {
    const el = card(file({ kind: 'image', name: 'chart.png' }), 'ses_1', false)
    expect(el.querySelector('a')).toBeNull()
    expect(el.querySelector('img')).toBeNull()
    expect(el.querySelector('.shared-file--inert')).not.toBeNull()
    expect(el.textContent).toContain('chart.png')
  })

  it('degrades the same way with no session to scope the id to', () => {
    const el = card(file(), '')
    expect(el.querySelector('a')).toBeNull()
    expect(el.querySelector('.shared-file--inert')).not.toBeNull()
  })

  // Not an edge case — the TTL sweeps every share eventually, and a transcript
  // is the thing you scroll back through weeks later. Before this, an expired
  // image rendered as underlined blue alt text beside a download that 404s.
  it('degrades when the bytes have been swept', async () => {
    const el = card(file({ kind: 'image', name: 'chart.png' }))
    const img = el.querySelector('img') as HTMLImageElement

    img.dispatchEvent(new Event('error'))
    await Promise.resolve()

    expect(el.querySelector('img')).toBeNull()
    expect(el.querySelector('a')).toBeNull()
    expect(el.textContent).toContain('chart.png')
    expect(el.textContent).toContain('no longer available')
  })
})

// The gap this closes. Before expires_at the only way a card learned its bytes
// were gone was an <img> failing to load — so it worked for images and for
// nothing else, while a document is the kind the tool leads with. These four
// cases are the four kinds, and three of them had no mechanism at all.
describe('SharedFileCard expiry', () => {
  const hourAgo = () => new Date(Date.now() - 3600_000).toISOString()
  const hourHence = () => new Date(Date.now() + 3600_000).toISOString()

  for (const kind of ['document', 'audio', 'video', 'image'] as const) {
    it(`stops offering a download for an expired ${kind}`, () => {
      const c = card(file({ kind, expires_at: hourAgo() }))
      expect(c.querySelector('.shared-file--inert')).toBeTruthy()
      expect(c.querySelectorAll('a')).toHaveLength(0)
      expect(c.querySelectorAll('audio, video, img')).toHaveLength(0)
      // It still says WHAT was shared — the record outlives the bytes.
      expect(c.textContent).toContain('report.pdf')
      expect(c.textContent).toContain('no longer available')
    })

    it(`keeps offering an unexpired ${kind}`, () => {
      const c = card(file({ kind, expires_at: hourHence() }))
      expect(c.querySelector('.shared-file--inert')).toBeNull()
      expect(c.querySelector('a.shared-file__row')).toBeTruthy()
    })
  }

  // A daemon older than the field sends nothing. Reading that as "expired"
  // would withdraw every download on a mixed-version deployment — the failure
  // mode is silent and total, so it gets its own case.
  it('treats a missing expiry as unknown, not as expired', () => {
    const c = card(file({ expires_at: undefined }))
    expect(c.querySelector('a.shared-file__row')).toBeTruthy()
    expect(c.querySelector('.shared-file--inert')).toBeNull()
  })

  // Same reasoning one level down: a string that will not parse is not evidence
  // about a file.
  it('treats an unparseable expiry as unknown', () => {
    const c = card(file({ expires_at: 'tuesday-ish' }))
    expect(c.querySelector('a.shared-file__row')).toBeTruthy()
  })

  // The deadline is an upper bound — cap eviction can take a file early — so
  // the image's own load failure stays as a second signal rather than being
  // replaced by it.
  it('still degrades an unexpired image whose bytes have already gone', async () => {
    const c = card(file({ kind: 'image', mime: 'image/png', expires_at: hourHence() }))
    const img = c.querySelector('img') as HTMLImageElement
    expect(img).toBeTruthy()
    img.dispatchEvent(new Event('error'))
    await Promise.resolve() // let the state update re-render
    expect(c.querySelector('.shared-file--inert')).toBeTruthy()
  })
})
