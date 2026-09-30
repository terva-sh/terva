import { useState } from 'preact/hooks'

// The person's name in a talkoot's room. The connection carries no identity,
// so the client states `by` on each verb that changes a team, and the room
// records it. It names who acted. It does not decide who may act: the steer
// capability does that.
const KEY = 'terva_talkoot_person'

// PERSON matches talkoot.ValidPerson: 1 to 64 letters, digits, and . _ @ -.
const PERSON = /^[A-Za-z0-9._@-]{1,64}$/

export function validPerson(name: string): boolean {
  return PERSON.test(name)
}

export function useTalkootPerson(): [string, (name: string) => void] {
  const [name, setName] = useState(() => localStorage.getItem(KEY) ?? '')
  const set = (next: string) => {
    const v = next.trim()
    setName(v)
    // A cleared or invalid name leaves storage too, so a reload does not
    // bring back a name the person removed.
    if (validPerson(v)) localStorage.setItem(KEY, v)
    else localStorage.removeItem(KEY)
  }
  return [name, set]
}
