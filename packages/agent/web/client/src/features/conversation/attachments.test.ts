import { afterEach, describe, expect, it, vi } from 'vitest'
import { fileToAttachment, maxImageBytes } from './attachments'

const file = (overrides: Partial<File> = {}) => ({
  type: 'image/png',
  size: 4,
  name: 'image.png',
  ...overrides,
}) as File

class FakeFileReader {
  static result = 'data:image/png;base64,QUJDRA=='
  static fail = false
  result: string | null = null
  onload: (() => void) | null = null
  onerror: (() => void) | null = null

  readAsDataURL() {
    if (FakeFileReader.fail) this.onerror?.()
    else {
      this.result = FakeFileReader.result
      this.onload?.()
    }
  }
}

describe('fileToAttachment', () => {
  afterEach(() => {
    FakeFileReader.result = 'data:image/png;base64,QUJDRA=='
    FakeFileReader.fail = false
    vi.unstubAllGlobals()
  })

  it('rejects unsafe image types before reading', async () => {
    await expect(fileToAttachment(file({ type: 'image/svg+xml' }))).resolves.toBeNull()
  })

  it('reports the existing localized size-limit error', async () => {
    await expect(fileToAttachment(file({ size: maxImageBytes + 1, name: 'large.png' }))).resolves.toEqual({
      error: 'large.png is too large (max 10 MB)',
    })
  })

  it('accepts a file exactly at the size limit (boundary is >, not >=)', async () => {
    vi.stubGlobal('FileReader', FakeFileReader)
    await expect(fileToAttachment(file({ size: maxImageBytes }))).resolves.toEqual({
      mime: 'image/png',
      data: 'QUJDRA==',
    })
  })

  it('extracts the base64 payload from a data URL', async () => {
    vi.stubGlobal('FileReader', FakeFileReader)
    await expect(fileToAttachment(file())).resolves.toEqual({ mime: 'image/png', data: 'QUJDRA==' })
  })

  it('returns null when reading fails or produces a malformed URL', async () => {
    vi.stubGlobal('FileReader', FakeFileReader)
    FakeFileReader.fail = true
    await expect(fileToAttachment(file())).resolves.toBeNull()
    FakeFileReader.fail = false
    FakeFileReader.result = 'not-a-data-url'
    await expect(fileToAttachment(file())).resolves.toBeNull()
  })
})
