// Repairs the Web Storage globals before any test runs.
//
// Node 25 exposes the Web Storage API as lazy globals. `localStorage` is one
// of them, and without a `--localstorage-file` its getter hands back a plain
// object instead of a Storage. `localStorage.clear` is then undefined, and
// every test that clears storage in a `beforeEach` dies with
// "TypeError: localStorage.clear is not a function". On this repository that
// was 29 tests across two files, and it fails the whole `just ci` gate on a
// current Node while CI, which installs an older Node from the alpine
// packages, stays green.
//
// happy-dom never gets a chance to install its own. The environment copies the
// window onto globalThis, `localStorage` is already there, and deleting the
// own property leaves nothing underneath rather than revealing happy-dom's.
// `sessionStorage` does survive, which is what makes the failure look
// arbitrary until you look at which names Node claims.
//
// So take a real Storage off a happy-dom Window and bind it over the top. That
// Window is the only source: the Storage constructor refuses a direct `new`,
// and building one from the prototype fails on its private fields with
// "Illegal invocation". A Window costs about 2ms, and this file builds one
// only on a Node that has the problem. Where the globals already work it
// constructs nothing, so CI pays nothing for it.

// This marks the file as a module. It has no static imports, because the one
// import it needs is dynamic and guarded below, and top-level await is legal
// only in a module.
export {}

type StorageName = 'localStorage' | 'sessionStorage'

const storageNames: StorageName[] = ['localStorage', 'sessionStorage']

// A working Storage answers clear(). Node's stand-in is an empty object, so
// this is the cheapest question that separates the two.
const isBroken = (name: StorageName): boolean => {
  const current = (globalThis as Record<string, unknown>)[name] as Storage | undefined
  return typeof current?.clear !== 'function'
}

const broken = storageNames.filter(isBroken)

if (broken.length > 0) {
  // Imported here rather than at the top, so a Node that leaves the globals
  // alone never loads happy-dom for this file at all.
  const { Window } = await import('happy-dom')
  // The donor window stays open on purpose: the Storage objects belong to it,
  // and closing it takes them with it. The worker drops both when the test
  // file ends.
  const donor = new Window()
  for (const name of broken) {
    Object.defineProperty(globalThis, name, {
      value: donor[name],
      configurable: true,
      writable: true,
    })
  }
}
