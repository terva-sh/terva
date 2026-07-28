import { useState } from 'preact/hooks'
import { t } from '../../i18n'
import type { SharedFile } from '../../platform/ctrlproto/types'
import { humanBytes } from '../../ui/formatting'

// SharedFileCard renders a file the AGENT handed to the user: an image to look
// at, a clip to play, or a chip to download.
//
// The mirror image of AttachedFiles, which is deliberately inert. This one is
// not: `id` resolves against the daemon's share store and GET /shared/ serves
// the bytes, so there is something real behind every affordance here. The two
// are separate components for that reason and not by accident — an inbound label
// promises nothing and an outbound card promises a file.
//
// Auth rides the terva_token cookie, exactly as /media/ already does for card
// avatars, so a bare src= and a bare href= both authenticate with no header. No
// token in any URL: it would land in browser history for every file ever shared.
export function SharedFileCard({ file, sess, canDownload }: { file: SharedFile; sess: string; canDownload: boolean }) {
  // Whether the bytes have gone. Shares are swept on a TTL, so this is not an
  // edge case — it is where EVERY shared file ends up, and a transcript is
  // exactly the thing you scroll back through weeks later.
  //
  // Two signals, and the ORDER matters. expires_at is the daemon's own answer
  // and needs no request, so it works for a document and a clip exactly as well
  // as for an image — which is the point, because for three of the four kinds
  // there is no element whose failure could tell us anything. The <img> error
  // below stays as a second signal, not the mechanism: it is free where it
  // applies and it catches the one case a deadline cannot, a file evicted early
  // to keep the area under its cap.
  const [failed, setFailed] = useState(false)

  // Without a route (a carrier that serves no downloads) or a session to scope
  // the id to, degrade to the inert label rather than to a broken image.
  if (!canDownload || !sess) return <InertShare file={file} />
  if (failed || pastExpiry(file.expires_at)) return <InertShare file={file} expired />

  const href = `/shared/${encodeURIComponent(sess)}/${encodeURIComponent(file.id)}`
  // ?inline=1 is what asks the daemon to render rather than download, and it
  // only obliges for a closed set of media types — an SVG or an HTML file comes
  // back as a download no matter what is asked, which is why the <img> below
  // cannot be turned into script execution by an agent picking an extension.
  const inline = href + '?inline=1'

  return (
    <div class="shared-file" data-kind={file.kind}>
      {file.kind === 'image' ? (
        <a class="shared-file__media" href={href} download={file.name} title={t('Download %s', file.name)}>
          <img src={inline} alt={file.name} loading="lazy" onError={() => setFailed(true)} />
        </a>
      ) : file.kind === 'audio' ? (
        <audio class="shared-file__media" controls preload="metadata" src={inline} />
      ) : file.kind === 'video' ? (
        <video class="shared-file__media" controls preload="metadata" src={inline} />
      ) : null}
      <a class="shared-file__row" href={href} download={file.name} title={file.mime ? `${file.name} — ${file.mime}` : file.name}>
        <span class="shared-file__icon" aria-hidden="true">
          ↓
        </span>
        <span class="shared-file__name">{file.name}</span>
        {file.size ? <span class="shared-file__size">{humanBytes(file.size)}</span> : null}
      </a>
      {file.caption ? <div class="shared-file__caption">{file.caption}</div> : null}
    </div>
  )
}

// pastExpiry reports a share the daemon has already told us it may have swept.
//
// Absent means UNKNOWN, not expired: a daemon older than the field sends
// nothing, and treating that as gone would silently withdraw every download on
// a mixed-version deployment. Unparseable is treated the same way, for the same
// reason — the failure of a string to parse is not evidence about a file.
//
// Compared against the BROWSER's clock, which may disagree with the daemon's. A
// skewed clock costs an early inert card or a late one, and the route is the
// backstop either way; it is not worth a round trip to do better.
export function pastExpiry(expiresAt?: string): boolean {
  if (!expiresAt) return false
  const at = Date.parse(expiresAt)
  return !isNaN(at) && at <= Date.now()
}

// InertShare is the degraded form: it still says what was shared, and does not
// pretend there is anywhere to get it. `expired` distinguishes "these bytes are
// gone" — the TTL doing its job — from "this client cannot fetch them", which
// are different things to tell someone.
function InertShare({ file, expired }: { file: SharedFile; expired?: boolean }) {
  return (
    <div class="shared-file shared-file--inert">
      <div class="shared-file__row">
        <span class="shared-file__name">{file.name}</span>
        {file.size ? <span class="shared-file__size">{humanBytes(file.size)}</span> : null}
      </div>
      {expired ? <div class="shared-file__caption">{t('no longer available — ask for it again')}</div> : null}
      {file.caption ? <div class="shared-file__caption">{file.caption}</div> : null}
    </div>
  )
}
