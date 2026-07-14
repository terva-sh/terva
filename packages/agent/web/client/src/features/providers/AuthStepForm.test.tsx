// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/preact'
import type { AuthFlowStep } from '../../platform/ctrlproto/types'
import { AuthStepForm } from './AuthStepForm'

afterEach(cleanup)

// The openai-compatible endpoint's real form: a base URL and a model, both
// required. It is the shape that produced the bug below — a valid base URL typed
// in, the model left blank, and a sign-in button that did nothing and said
// nothing.
const compatible: AuthFlowStep = {
  flow: 'flow-1',
  kind: 'form',
  title: 'OpenAI-compatible',
  fields: [
    { name: 'base_url', label: 'Base URL', type: 'text', required: true, placeholder: 'http://localhost:1234/v1' },
    { name: 'model', label: 'Default model', type: 'text', required: true, placeholder: 'qwen2.5-coder' },
    { name: 'context_window', label: 'Context window', type: 'integer' },
  ],
}

// The real form: naming the endpoint makes it its own provider, which discovers
// its own models — so the default model stops being required.
const named: AuthFlowStep = {
  flow: 'flow-2',
  kind: 'form',
  fields: [
    { name: 'name', label: 'Name', type: 'text', placeholder: 'workshop-3090' },
    { name: 'base_url', label: 'Base URL', type: 'text', required: true, placeholder: 'http://localhost:1234/v1' },
    { name: 'model', label: 'Default model', type: 'text', required: true, required_unless: 'name', placeholder: 'qwen2.5-coder' },
  ],
}

const signIn = () => screen.getByText('Sign in') as HTMLButtonElement

describe('AuthStepForm', () => {
  // The regression. A disabled control is not an explanation: the form must name
  // what it is still waiting for, or a filled-in base URL with an empty model is
  // indistinguishable from a broken button.
  it('names the required fields still outstanding instead of going silently dead', () => {
    render(<AuthStepForm step={compatible} onSubmit={() => {}} onCancel={() => {}} busy={false} error="" />)

    expect(signIn().disabled).toBe(true)
    expect(screen.getByText(/Still needed:/).textContent).toContain('Base URL')

    fireEvent.input(screen.getByPlaceholderText('http://localhost:1234/v1'), {
      target: { value: 'https://example.invalid/v1' },
    })

    // The base URL is satisfied, so it must drop out of the message — and the
    // model, the one that actually bit, must be named on its own.
    const note = screen.getByText(/Still needed:/).textContent ?? ''
    expect(note).toContain('Default model')
    expect(note).not.toContain('Base URL')
    expect(signIn().disabled).toBe(true)
  })

  it('enables sign-in once every required field is filled, and submits what was typed', () => {
    const onSubmit = vi.fn()
    render(<AuthStepForm step={compatible} onSubmit={onSubmit} onCancel={() => {}} busy={false} error="" />)

    fireEvent.input(screen.getByPlaceholderText('http://localhost:1234/v1'), {
      target: { value: 'https://example.invalid/v1' },
    })
    fireEvent.input(screen.getByPlaceholderText('qwen2.5-coder'), { target: { value: 'qwen2.5-coder' } })

    // Nothing is outstanding, so the form stops nagging and the button lives.
    expect(screen.queryByText(/Still needed:/)).toBeNull()
    expect(signIn().disabled).toBe(false)

    fireEvent.click(signIn())
    expect(onSubmit).toHaveBeenCalledWith({
      base_url: 'https://example.invalid/v1',
      model: 'qwen2.5-coder',
      // Optional and untouched: it still travels, and the daemon stays the only
      // authority on what an empty context window means.
      context_window: '',
    })
  })

  // Whitespace is not a value. Without the trim the button would unlock on a
  // stray space and the daemon would reject a login the form had called complete.
  it('does not count a whitespace-only entry as filled', () => {
    render(<AuthStepForm step={compatible} onSubmit={() => {}} onCancel={() => {}} busy={false} error="" />)
    fireEvent.input(screen.getByPlaceholderText('qwen2.5-coder'), { target: { value: '   ' } })
    expect(screen.getByText(/Still needed:/).textContent).toContain('Default model')
    expect(signIn().disabled).toBe(true)
  })

  // While a login is in flight the button already explains itself ("Signing in…");
  // a "still needed" line underneath it would be noise about fields nobody can edit.
  it('says nothing about missing fields while a login is in flight', () => {
    render(<AuthStepForm step={compatible} onSubmit={() => {}} onCancel={() => {}} busy={true} error="" />)
    expect(screen.queryByText(/Still needed:/)).toBeNull()
    expect(screen.getByText('Signing in…')).toBeTruthy()
  })

  // required_unless. Naming the endpoint must retire the model requirement — a
  // named endpoint runs its own /v1/models discovery, so demanding a model id
  // would be asking the operator to paste back what terva is about to find.
  it('stops requiring the default model once the endpoint is named', () => {
    render(<AuthStepForm step={named} onSubmit={() => {}} onCancel={() => {}} busy={false} error="" />)

    fireEvent.input(screen.getByPlaceholderText('http://localhost:1234/v1'), {
      target: { value: 'http://3090.box:8000/v1' },
    })
    // Unnamed: the model is still the one thing outstanding.
    expect(screen.getByText(/Still needed:/).textContent).toContain('Default model')
    expect(signIn().disabled).toBe(true)

    fireEvent.input(screen.getByPlaceholderText('workshop-3090'), { target: { value: 'workshop-3090' } })

    // Named: the requirement is gone, and the form says so by letting the button live.
    expect(screen.queryByText(/Still needed:/)).toBeNull()
    expect(signIn().disabled).toBe(false)
  })

  // The label has to track the CURRENT requirement, not the declared one, or it
  // goes on demanding a field the daemon will not ask for.
  it('marks the default model optional once the endpoint is named', () => {
    const { container } = render(
      <AuthStepForm step={named} onSubmit={() => {}} onCancel={() => {}} busy={false} error="" />,
    )
    const modelLabel = () =>
      [...container.querySelectorAll('.prov-label')].find((el) => el.textContent?.startsWith('Default model'))

    expect(modelLabel()?.textContent).toBe('Default model')

    fireEvent.input(screen.getByPlaceholderText('workshop-3090'), { target: { value: 'little-box' } })
    expect(modelLabel()?.textContent).toContain('(optional)')
  })
})
