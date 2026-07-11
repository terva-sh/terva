import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyToClipboard } from './browser'

afterEach(() => vi.unstubAllGlobals())

describe('copyToClipboard', () => {
  it('uses the modern clipboard API when available', async () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    expect(await copyToClipboard('hello')).toBe(true)
    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('falls back to a hidden textarea when the modern API fails', async () => {
    const textarea = { value: '', style: {}, setAttribute: vi.fn(), select: vi.fn() }
    const appendChild = vi.fn()
    const removeChild = vi.fn()
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => { throw new Error('denied') }) } })
    vi.stubGlobal('document', {
      createElement: vi.fn(() => textarea),
      body: { appendChild, removeChild },
      execCommand: vi.fn(() => true),
    })

    expect(await copyToClipboard('fallback')).toBe(true)
    expect(textarea.value).toBe('fallback')
    expect(textarea.select).toHaveBeenCalled()
    expect(appendChild).toHaveBeenCalledWith(textarea)
    expect(removeChild).toHaveBeenCalledWith(textarea)
  })

  it('returns false when neither clipboard path works', async () => {
    vi.stubGlobal('navigator', {})
    vi.stubGlobal('document', { createElement: vi.fn(() => { throw new Error('unavailable') }) })
    expect(await copyToClipboard('nope')).toBe(false)
  })
})
