// @vitest-environment happy-dom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/preact'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fakeClient } from '../../platform/ctrlproto/testing'
import { SaveTemplate } from './SaveTemplate'

afterEach(cleanup)

describe('save as template', () => {
  it('sends the team, the name, and the description, then says the form lists it', async () => {
    const client = fakeClient({ respond: () => Promise.resolve({ name: 'our-pair', source: 'user', members: 2 }) })
    render(<SaveTemplate client={client} id="pair" onError={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save as template' }))
    expect((screen.getByLabelText('Template name') as HTMLInputElement).value).toBe('pair')
    fireEvent.input(screen.getByLabelText('Template name'), { target: { value: 'our-pair' } })
    fireEvent.input(screen.getByPlaceholderText('optional'), { target: { value: 'Our pair.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(client.last('talkoot.save_template')?.params).toEqual({ id: 'pair', name: 'our-pair', description: 'Our pair.' }),
    )
    await waitFor(() => expect(screen.getByText(/Saved as our-pair/)).toBeTruthy())
  })

  it('refuses a name that is not an id before it sends', () => {
    const client = fakeClient()
    render(<SaveTemplate client={client} id="pair" onError={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save as template' }))
    fireEvent.input(screen.getByLabelText('Template name'), { target: { value: 'Our_Pair' } })
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true)
    expect(client.sent('talkoot.save_template')).toHaveLength(0)
  })

  it('shows a refusal and keeps the form open', async () => {
    const client = fakeClient({ respond: () => Promise.reject(new Error('a template of that name exists')) })
    const onError = vi.fn()
    render(<SaveTemplate client={client} id="pair" onError={onError} />)
    fireEvent.click(screen.getByRole('button', { name: 'Save as template' }))
    fireEvent.input(screen.getByLabelText('Template name'), { target: { value: 'coding' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onError).toHaveBeenCalledWith('a template of that name exists'))
    expect(screen.getByRole('button', { name: 'Save' })).toBeTruthy()
  })
})
