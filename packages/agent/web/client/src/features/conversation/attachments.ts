import { t } from '../../i18n'
import { isSafeImageMime, type ImageAttachment } from '../../platform/conversation/images'
import { authToken } from '../../platform/ctrlproto/client'

// An image rides the prompt frame INLINE, because vision needs the bytes in the
// turn. The frame is capped at 32 MiB and base64 inflates by ~4/3, so a 10 MiB
// raw image leaves room for the JSON envelope and the prompt text beside it.
//
// Anything larger — and anything that is not an image — goes to the upload route
// instead, where the ceiling is the daemon's and the bytes never enter a frame.
export const maxImageBytes = 10 * 1024 * 1024

// fileToAttachment reads an allowlisted image into its base64 wire payload.
// Returns null when the file is not an inline-able image, which is the caller's
// signal to stage it as a file attachment instead.
export function fileToAttachment(f: File): Promise<ImageAttachment | null> {
  if (!isSafeImageMime(f.type) || f.size > maxImageBytes) return Promise.resolve(null)
  return new Promise((resolve) => {
    const reader = new FileReader()
    reader.onload = () => {
      const url = String(reader.result)
      const comma = url.indexOf(',')
      resolve(comma < 0 ? null : { mime: f.type, data: url.slice(comma + 1) })
    }
    reader.onerror = () => resolve(null)
    reader.readAsDataURL(f)
  })
}

// FileAttachment is one file staged with the daemon: what the composer shows in
// its chip, and the id the prompt will name.
//
// There is no path here on purpose. The client's handle on a staged file is its
// id — the daemon resolves the path itself — so the browser is never told where
// on the host the file landed.
export interface FileAttachment {
  id: string
  name: string
  mime: string
  kind: string
  size: number
}

// uploadFile stages a file with the daemon and returns the reference to name on
// the next prompt.
//
// The bytes go over HTTP rather than the WebSocket: a 100 MB upload does not fit
// in a frame, and even when it did it would stall every control message queued
// behind it. The response is JSON either way, so a refusal (too large, no
// session, expired credential) arrives as something the composer can show
// instead of an opaque status code.
export async function uploadFile(f: File, sess: string): Promise<FileAttachment | { error: string }> {
  const body = new FormData()
  body.append('file', f, f.name)
  const token = authToken()
  const url = `/upload?sess=${encodeURIComponent(sess)}` + (token ? `&token=${encodeURIComponent(token)}` : '')
  let res: Response
  try {
    res = await fetch(url, { method: 'POST', body, credentials: 'same-origin', cache: 'no-store' })
  } catch {
    // A network failure, not a refusal — the daemon never saw the file.
    return { error: t('%s could not be uploaded', f.name || t('file')) }
  }
  const payload = (await res.json().catch(() => null)) as
    | (FileAttachment & { error?: string })
    | null
  if (!res.ok || !payload?.id) {
    return { error: payload?.error || t('%s could not be uploaded', f.name || t('file')) }
  }
  return payload
}

// tooLargeToAttach reports a file the daemon would refuse, so the composer can
// say so before spending a long upload on it. A zero limit means the carrier did
// not advertise one; let the daemon be the judge rather than inventing a bound.
export function tooLargeToAttach(f: File, maxAttachmentBytes: number): boolean {
  return maxAttachmentBytes > 0 && f.size > maxAttachmentBytes
}
