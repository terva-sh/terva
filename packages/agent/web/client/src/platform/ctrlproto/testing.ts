import { type Mock, vi } from 'vitest'

import type { ClientLike, ConnectableClient } from './client'
import type { ParamsFor, Verb, WireEvent } from './types'

// A ctrlproto stub that is actually type-checked.
//
// Every test used to write `{ send: vi.fn(), fire: vi.fn() } as unknown as
// Client`, because Client's private fields make it nominal and no plain object
// can satisfy it. That cast switches checking off completely: the stub could
// omit a method a component calls, or keep one the interface had since renamed,
// and every test using it still passed — a stub drifting from the thing it
// stands in for, which is the failure this whole branch is about.
//
// fakeClient returns a value the compiler holds to ClientLike, so a change to
// the transport surface breaks here loudly. `send` and `fire` ARE the vi mocks,
// so `expect(client.send).toHaveBeenCalledWith(...)` reads exactly as before;
// `sent()` / `last()` are the typed alternative when the assertion is about one
// verb's params.
//
// There is one cast, below, and it is the point: the ten it replaces were spread
// across five files, each independently able to rot.

export interface SentCommand<M extends Verb = Verb> {
  method: M
  params: ParamsFor<M>
  sess: string
}

export interface FakeClient extends ConnectableClient {
  send: ClientLike['send'] & Mock
  fire: ClientLike['fire'] & Mock
  connect: (() => void) & Mock
  close: (() => void) & Mock
  /** True between connect() and close(), so a test can assert lifecycle. */
  readonly isConnected: () => boolean
  /** Every send/fire, in order. */
  readonly commands: SentCommand[]
  /** The commands for one verb, typed to that verb's params. */
  sent<M extends Verb>(method: M): SentCommand<M>[]
  /** The most recent command for one verb, or undefined. */
  last<M extends Verb>(method: M): SentCommand<M> | undefined
  /** Deliver an event as the daemon would, to whatever handler is installed. */
  emit(sess: string, ev: WireEvent): void
}

export interface FakeClientOptions {
  /**
   * Answers a send. Return the result, or a Promise. Throwing (or returning a
   * rejected promise) surfaces as a rejected send, which is how to drive a
   * component's error path. Default: resolve `{}` for everything.
   */
  respond?: (method: Verb, params: unknown, sess: string) => unknown
  /** Seed for maxUploadBytes; 0 (the default) means unbounded, as when cold. */
  maxUploadBytes?: number
  /** Seed for maxAttachmentBytes; 0 (the default) means the carrier didn't say. */
  maxAttachmentBytes?: number
  /** Whether this fake carrier claims an upload route (ctrlproto "attachments"). */
  canAttachFiles?: boolean
}

export function fakeClient(opts: FakeClientOptions = {}): FakeClient {
  const commands: SentCommand[] = []
  const record = (method: Verb, params: unknown, sess: string) => {
    commands.push({ method, params: params as ParamsFor<Verb>, sess })
  }

  // The single cast. vi.fn cannot express send's generic signature, so the mock
  // is built with the erased one and asserted into place here — once, where the
  // shape is visible, instead of at every call site.
  const send = vi.fn((method: Verb, params: unknown, sess = '') => {
    record(method, params, sess)
    // A throw becomes a rejection — the daemon answers with an error frame, it
    // does not make send() throw synchronously. Caught here rather than inside a
    // .then() so the returned promise is settled at the same microtask depth as
    // vi.fn().mockResolvedValue/mockRejectedValue: an extra hop would make
    // existing `await Promise.resolve()` flushes silently insufficient, i.e. a
    // test double whose timing differs from the thing it replaced.
    try {
      return Promise.resolve(opts.respond ? opts.respond(method, params, sess) : {})
    } catch (e) {
      return Promise.reject(e)
    }
  }) as unknown as FakeClient['send']

  const fire = vi.fn((method: Verb, params: unknown, sess = '') => {
    record(method, params, sess)
  }) as unknown as FakeClient['fire']

  let connected = false
  const connect = vi.fn(() => {
    connected = true
  }) as unknown as FakeClient['connect']
  const close = vi.fn(() => {
    connected = false
  }) as unknown as FakeClient['close']

  const client: FakeClient = {
    maxUploadBytes: opts.maxUploadBytes ?? 0,
    maxAttachmentBytes: opts.maxAttachmentBytes ?? 0,
    canAttachFiles: opts.canAttachFiles ?? false,
    onEvent: () => {},
    onStatus: () => {},
    onReady: () => {},
    send,
    fire,
    connect,
    close,
    isConnected: () => connected,
    commands,
    sent<M extends Verb>(method: M) {
      return commands.filter((c) => c.method === method) as SentCommand<M>[]
    },
    last<M extends Verb>(method: M) {
      const all = client.sent(method)
      return all.length ? all[all.length - 1] : undefined
    },
    emit(sess: string, ev: WireEvent) {
      client.onEvent(sess, ev)
    },
  }
  return client
}
