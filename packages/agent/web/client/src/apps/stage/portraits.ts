// Whether Stage shows character portraits at all.
//
// Character cards arrive with art, and the art is often the point — but not
// always, and not everywhere. Someone reading in a shared room, someone whose
// library is mostly imported covers they did not choose, someone who just wants
// to find a name in a list of eighty: for all of them the pictures are the noise
// and the names are the signal.
//
// A client-local preference on the document root, the same shape as theme.ts and
// for the same reason: it has to reach every surface at once, and Stage owns its
// own shell. CSS keys off the attribute (stage.css, `[data-portraits='off']`)
// rather than each component branching on a prop — a prop would have to be
// threaded through the Library, the studio, the cast strip, and the sheets, and
// the one that got missed would be the one still showing a face.
//
// Off means the tiles KEEP their shape and lose the picture. The grid geometry
// is what makes the shelf scannable; a mode that also reflowed it would be a
// different view rather than the same view with the images turned off.

const KEY = 'stage-portraits'
const ATTR = 'data-portraits'

// portraitsOn reports the persisted preference. Portraits are ON unless the
// author turned them off — a library whose art silently vanished after an
// upgrade would read as broken, not as configured.
export function portraitsOn(): boolean {
  try {
    return localStorage.getItem(KEY) !== 'off'
  } catch {
    return true
  }
}

// applyPortraits stamps the root (re-rendering live) and persists the choice.
// The attribute is REMOVED rather than set to "on", so the default state has no
// marker and a stylesheet cannot accidentally key off the wrong value.
export function applyPortraits(on: boolean): void {
  if (on) {
    document.documentElement.removeAttribute(ATTR)
  } else {
    document.documentElement.setAttribute(ATTR, 'off')
  }
  try {
    localStorage.setItem(KEY, on ? 'on' : 'off')
  } catch {
    // A private-mode / storage-blocked browser still honours it this session.
  }
}
