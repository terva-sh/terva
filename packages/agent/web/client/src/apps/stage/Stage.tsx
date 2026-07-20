import { useEffect, useRef, useState } from 'preact/hooks'
import { Client } from '../../platform/ctrlproto/client'
import type { Status } from '../../platform/ctrlproto/types'
import { Library } from './Library'
import { Chat } from './Chat'

// The Stage app owns one shared platform Client and switches between two screens
// with plain state — no URL routing, so nothing depends on the /stage/ path depth
// (the embedded app serves assets relative to /stage/). The library is the
// landing; a chat is opened over it and dismissed back to it.
type View = { screen: 'library' } | { screen: 'chat'; session: string }

export function Stage() {
  const [status, setStatus] = useState<Status>('connecting')
  const [ready, setReady] = useState(false)
  // generation counts CONNECTIONS, not renders. Every hello — the first and every
  // one after a reconnect — bumps it, and the chat's subscription effect depends on
  // it. Server-side subscriptions live and die with the socket, so without this a
  // reconnected Stage held a subscription the daemon no longer had: turns still ran
  // (the socket was open) but nothing came back, so neither the user's message nor
  // the reply ever rendered and only a reload recovered.
  const [generation, setGeneration] = useState(0)
  const [view, setView] = useState<View>({ screen: 'library' })

  const clientRef = useRef<Client | null>(null)
  if (!clientRef.current) clientRef.current = new Client()
  const client = clientRef.current

  useEffect(() => {
    client.onStatus = setStatus
    client.onReady = () => {
      setReady(true)
      setGeneration((g) => g + 1)
    }
    client.connect()
    return () => client.close()
  }, [client])

  if (view.screen === 'chat') {
    // key by session so a branch (which switches to a new session) remounts the
    // chat with clean local state instead of carrying the old draft/edit over.
    return (
      <Chat
        key={view.session}
        client={client}
        sessionId={view.session}
        generation={generation}
        onBack={() => setView({ screen: 'library' })}
        onOpenSession={(session) => setView({ screen: 'chat', session })}
      />
    )
  }
  return (
    <Library
      client={client}
      ready={ready}
      status={status}
      onOpenChat={(session) => setView({ screen: 'chat', session })}
    />
  )
}
