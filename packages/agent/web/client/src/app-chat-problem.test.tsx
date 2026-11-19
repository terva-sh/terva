// @vitest-environment happy-dom
import { cleanup, render, screen } from '@testing-library/preact'
import { afterEach, describe, expect, it } from 'vitest'
import { ChatBody } from './app'

afterEach(cleanup)

// A talkoot_chat key that starts no bridge left this pane at idle with no
// reason. With Talkoot off no team has a view, so this pane is the one place
// in the app that can say why.
describe('the chat pane, for a talkoot_chat key that starts no bridge', () => {
  it('shows the reason', () => {
    render(
      <ChatBody
        v={{ bridge: { state: 'idle' }, problem: 'talkoot_chat is set, and talkoot_enabled is off' }}
        onAction={() => {}}
      />,
    )
    expect(screen.getByText('No chat bridge for a team: talkoot_chat is set, and talkoot_enabled is off')).toBeTruthy()
  })

  it('shows no such line when the daemon reports no problem', () => {
    render(<ChatBody v={{ bridge: { state: 'idle' } }} onAction={() => {}} />)
    expect(screen.queryByText(/No chat bridge for a team/)).toBeNull()
  })
})
