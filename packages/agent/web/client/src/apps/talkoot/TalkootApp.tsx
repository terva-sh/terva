import { useEffect, useRef, useState } from 'preact/hooks'
import { Client } from '../../platform/ctrlproto/client'
import { ADDR_WORKSPACE, type CatalogView, type Status } from '../../platform/ctrlproto/types'
import { TalkootHub } from '../../platform/talkoot/hub'
import { applyServerCatalog, setLocale, t } from '../../i18n'
import { panelHref } from '../../ui/navlinks'
import { Shell } from './Shell'

// What the app knows about the daemon: nothing yet, a daemon that runs
// talkoots, or one that does not grant the talkoot group to this connection.
type Grant = 'unknown' | 'granted' | 'refused'

// TalkootApp owns one Client and one TalkootHub. The panel shares its client's
// single onEvent between sessions and talkoots, so it forwards each #talkoot:
// address to its hub. Here the hub is the only reader, and onEvent goes to it
// whole.
export function TalkootApp() {
  const [status, setStatus] = useState<Status>('connecting')
  const [grant, setGrant] = useState<Grant>('unknown')
  // generation counts connections. A subscription lives on one socket, so the
  // team view subscribes again and reads again on every new one.
  const [generation, setGeneration] = useState(0)
  // i18nEpoch re-renders when the server catalog lands, without touching
  // generation, which would resubscribe.
  const [, setI18nEpoch] = useState(0)

  const clientRef = useRef<Client | null>(null)
  if (!clientRef.current) clientRef.current = new Client()
  const client = clientRef.current
  const [hub] = useState(() => new TalkootHub())

  useEffect(() => {
    client.onStatus = setStatus
    client.onEvent = (addr, ev) => {
      hub.dispatch(addr, ev)
    }
    client.onReady = (hello) => {
      document.documentElement.lang = setLocale(hello?.locale ?? 'en')
      client
        .send<{ catalog: CatalogView }>('i18n.catalog', {}, '')
        .then((r) => {
          if (!r?.catalog) return
          document.documentElement.lang = applyServerCatalog(r.catalog)
          setI18nEpoch((e) => e + 1)
        })
        .catch(() => {
          /* an older daemon, or offline: the bundled catalog stands in */
        })
      const ok = !!hello?.groups?.includes('talkoot')
      setGrant(ok ? 'granted' : 'refused')
      // talkoots_changed rides #workspace, so the team list hears a new team.
      if (ok && hello?.features?.includes('workspace-events')) client.fire('subscribe', null, ADDR_WORKSPACE)
      setGeneration((g) => g + 1)
    }
    client.connect()
    return () => client.close()
  }, [client, hub])

  if (grant === 'refused') {
    return (
      <div class="talkoot-app-note">
        <p>{t('This daemon does not run talkoots, so there is nothing to show here.')}</p>
        <p>{t('Set talkoot_enabled in your user configuration and restart the daemon to use the Talkoot app.')}</p>
        <a class="btn" href={panelHref()}>
          {t('Open the control panel')}
        </a>
      </div>
    )
  }
  if (grant === 'unknown') {
    return <div class="talkoot-app-note">{status === 'connecting' ? t('Connecting…') : t('Waiting for the daemon…')}</div>
  }
  return (
    <div class="talkoot-app">
      <Shell client={client} hub={hub} generation={generation} />
    </div>
  )
}
