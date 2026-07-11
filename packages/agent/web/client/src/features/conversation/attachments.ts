import { t } from '../../i18n'
import { isSafeImageMime, type ImageAttachment } from '../../platform/conversation/images'

// One frame is capped at 16 MiB; base64 inflates by ~4/3 and JSON/prompt text
// need room, so a 10 MiB raw image stays comfortably below the carrier limit.
export const maxImageBytes = 10 * 1024 * 1024

// fileToAttachment reads an allowlisted image into its base64 wire payload.
export function fileToAttachment(f: File): Promise<ImageAttachment | { error: string } | null> {
  if (!isSafeImageMime(f.type)) return Promise.resolve(null)
  if (f.size > maxImageBytes) {
    return Promise.resolve({ error: t('%s is too large (max 10 MB)', f.name || t('image')) })
  }
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
