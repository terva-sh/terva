// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { CopyButton } from './CopyButton'
import { ImageGallery } from './ImageGallery'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('CopyButton', () => {
  it('copies text and shows success feedback', async () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    render(<CopyButton text="raw markdown" label="Copy reply" />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy reply' }))
    expect(writeText).toHaveBeenCalledWith('raw markdown')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Copied' }).classList.contains('copied')).toBe(true))
  })

  it('keeps its original state when copying fails', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn(async () => { throw new Error('denied') }) } })
    render(<CopyButton text="text" />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(screen.getByRole('button', { name: 'Copy' }).classList.contains('copied')).toBe(false)
  })
})

describe('ImageGallery', () => {
  it('renders only allowlisted image MIME types as self-contained links', () => {
    const { container } = render(
      <ImageGallery images={[{ mime: 'image/png', data: 'cG5n' }, { mime: 'image/svg+xml', data: 'PHN2Zz4=' }]} />,
    )
    const image = screen.getByAltText('attached image') as HTMLImageElement
    const link = image.closest('a')
    expect(image.src).toBe('data:image/png;base64,cG5n')
    expect(link?.getAttribute('href')).toBe('data:image/png;base64,cG5n')
    expect(link?.getAttribute('target')).toBe('_blank')
    expect(container.querySelectorAll('img')).toHaveLength(1)
  })
})
