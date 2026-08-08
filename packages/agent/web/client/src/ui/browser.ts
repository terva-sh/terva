// A file the daemon serialized for download — the shape cards.export,
// worlds.export and sessions.export all return. `bytes` is base64 on the wire.
export type DownloadPayload = { filename: string; mime_type: string; bytes: string }

// Saves an export payload to the user's disk.
//
// Extracted because this was written out twice verbatim (the card sheet and the
// world sheet) and a third export was about to copy it again. The base64 decode
// is the part worth having in one place: `atob` yields a binary STRING, and
// handing that to Blob directly would re-encode it as UTF-8 and corrupt every
// byte above 0x7f — silently, and only for cards with avatars or any non-ASCII
// content.
export function downloadExport(res: DownloadPayload): void {
  const bin = atob(res.bytes)
  const arr = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i)
  const url = URL.createObjectURL(new Blob([arr], { type: res.mime_type }))
  const a = document.createElement('a')
  a.href = url
  a.download = res.filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// Base64-encodes a file's bytes for the *.import verbs (Go decodes []byte from
// base64).
//
// Extracted for the same reason downloadExport was, and after the same near
// miss: two copies existed (the Library and the steering drawer) and the World
// studio's cover upload was about to be a third. They had already drifted — one
// built the binary string a character at a time, which is slow enough to matter
// on a multi-megabyte portrait. This is the chunked one; CHUNK stays well under
// the argument limit that String.fromCharCode(...) blows past on a large card.
export async function fileToBase64(file: File): Promise<string> {
  const buf = new Uint8Array(await file.arrayBuffer())
  const CHUNK = 0x8000
  const parts: string[] = []
  for (let i = 0; i < buf.length; i += CHUNK) {
    parts.push(String.fromCharCode(...buf.subarray(i, i + CHUNK)))
  }
  return btoa(parts.join(''))
}

// Writes text to the clipboard, falling back to a hidden textarea for insecure
// plain-HTTP contexts where navigator.clipboard is unavailable.
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Permission denied or non-secure context — use the legacy path.
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '-1000px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
