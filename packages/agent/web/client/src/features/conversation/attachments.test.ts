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

  // null is no longer "we threw this away" — it is "this is not inline-able,
  // stage it as a file instead", which is what the composer does with it. An SVG
  // stays off the inline path (in a data:/blob: context it is a script
  // container, not merely an image) but is a perfectly good attachment.
  it('refuses unsafe image types the inline path, leaving them to be staged', async () => {
    await expect(fileToAttachment(file({ type: 'image/svg+xml' }))).resolves.toBeNull()
  })

  // Over the inline budget is not an error any more: the file is too big for a
  // wire frame, which is exactly what the upload route exists for.
  it('refuses an oversized image the inline path rather than erroring', async () => {
    await expect(fileToAttachment(file({ size: maxImageBytes + 1, name: 'large.png' }))).resolves.toBeNull()
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
