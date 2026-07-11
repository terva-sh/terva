// ImageAttachment is one rendered image (data: URL source), carried on the
// message/tool items whose wire blocks included image payloads.
export interface ImageAttachment {
  mime: string
  data: string // base64
}

// Raster formats a browser can only draw. SVG is excluded because an SVG in a
// data:/blob: context is a script container, not merely an image.
const safeImageMimes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

// isSafeImageMime tolerates parameters and case ("image/PNG; charset=binary").
export function isSafeImageMime(mime: string | undefined): boolean {
  if (!mime) return false
  const bare = mime.toLowerCase().split(';')[0].trim()
  return safeImageMimes.has(bare)
}
