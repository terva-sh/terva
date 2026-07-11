import { t } from '../i18n'
import { type ImageAttachment, isSafeImageMime } from '../platform/conversation/images'

// ImageGallery renders a message's image attachments as inline thumbnails that
// open full-size in a new tab. Data is base64 already on the item, so the src is
// a self-contained data: URL — no extra fetch.
export function ImageGallery({ images }: { images: ImageAttachment[] }) {
  // Belt-and-suspenders: the store filters wire blocks and the upload path
  // filters files, but anything that reaches this data:-URL sink gets the
  // same allowlist check — no unvetted MIME ever lands in an href/src.
  const safe = images.filter((image) => isSafeImageMime(image.mime))
  if (!safe.length) return null
  return (
    <div class="msg-images">
      {safe.map((image, index) => {
        const src = `data:${image.mime};base64,${image.data}`
        return (
          <a key={index} class="msg-image-link" href={src} target="_blank" rel="noreferrer">
            <img class="msg-image" src={src} alt={t('attached image')} loading="lazy" />
          </a>
        )
      })}
    </div>
  )
}
