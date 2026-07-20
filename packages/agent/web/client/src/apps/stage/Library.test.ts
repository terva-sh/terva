import { describe, expect, it } from 'vitest'
import { formatBytes, importError } from './Library'

describe('formatBytes', () => {
  it('scales to the unit a person would use', () => {
    expect(formatBytes(512)).toBe('512 bytes')
    expect(formatBytes(4096)).toBe('4 KB')
    expect(formatBytes(19647777)).toBe('18.7 MB')
  })
})

describe('importError', () => {
  // The bug this whole path exists for: an oversized upload is not answered with
  // an error, it drops the socket — so the raw rejection ("not connected" /
  // "connection closed") names nothing the user can act on. Both spellings must
  // become an explanation that mentions size.
  it('explains a dropped socket and names the file size', () => {
    const file = { size: 19647777 } as File
    for (const raw of ['not connected', 'connection closed']) {
      const msg = importError(new Error(raw), file)
      expect(msg).toContain('18.7 MB')
      expect(msg).toMatch(/size/i)
      expect(msg).not.toBe(raw)
    }
  })

  it('still explains a dropped socket with no file to measure', () => {
    const msg = importError(new Error('connection closed'))
    expect(msg).toMatch(/lost the connection/i)
    expect(msg).not.toContain('undefined')
  })

  // The daemon's own rejections are already good sentences. Keep the reason,
  // drop the wire code in front of it.
  it('strips the transport prefix from a daemon rejection', () => {
    expect(importError(new Error('badRequest: import card: no character metadata (`chara` chunk) in PNG'))).toBe(
      'import card: no character metadata (`chara` chunk) in PNG',
    )
  })

  it('handles a non-Error rejection', () => {
    expect(importError('something broke')).toBe('something broke')
  })
})
