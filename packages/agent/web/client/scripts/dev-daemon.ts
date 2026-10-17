// The dev server's link to a running daemon. With TERVA_DEV_DAEMON set to the
// daemon's web address, such as http://127.0.0.1:8730, `npm run dev` proxies
// the daemon's routes, so a page served from source talks to real sessions and
// teams. Without it the dev server proxies nothing.
//
// 🔑 The daemon refuses a request whose Origin names another host (sameOrigin
// in packages/agent/web/web.go). A page from the dev server sends the dev
// server's own origin, so the proxy rewrites that one origin to the daemon's.
// Any other origin passes through unchanged, and the daemon still refuses it.
// Rewriting every origin would let any page in the browser reach the daemon
// through the dev server.

import type { ProxyOptions } from 'vite'

// DAEMON_ROUTES are the daemon's routes that the client calls by path. Vite
// matches each one as a prefix, so /auth covers /auth/status too.
export const DAEMON_ROUTES = ['/ws', '/auth', '/media/', '/upload', '/shared/']

type Headers = Record<string, string | string[] | undefined>

// daemonOrigin returns the Origin to send the daemon, or undefined to leave the
// header as the browser sent it. Only an origin that names the host the request
// came in on, which is the dev server, is rewritten.
export function daemonOrigin(headers: Headers, daemon: string): string | undefined {
  const { origin, host } = headers
  if (typeof origin !== 'string' || typeof host !== 'string') return undefined
  let u: URL
  try {
    u = new URL(origin)
  } catch {
    return undefined
  }
  if (u.host.toLowerCase() !== host.toLowerCase()) return undefined
  return new URL(daemon).origin
}

// daemonProxy is the dev server's proxy table for a daemon at daemon.
export function daemonProxy(daemon: string): Record<string, ProxyOptions> {
  let target: URL
  try {
    target = new URL(daemon)
  } catch {
    throw new Error(`TERVA_DEV_DAEMON=${daemon} is not a URL; use the daemon's web address, such as http://127.0.0.1:8730`)
  }
  if (target.protocol !== 'http:' && target.protocol !== 'https:') {
    throw new Error(`TERVA_DEV_DAEMON=${daemon} must be an http or https address`)
  }
  const configure: ProxyOptions['configure'] = (proxy) => {
    const rewrite = (proxyReq: { setHeader(name: string, value: string): unknown }, req: { headers: Headers }) => {
      const origin = daemonOrigin(req.headers, target.href)
      if (origin) proxyReq.setHeader('origin', origin)
    }
    proxy.on('proxyReq', rewrite)
    proxy.on('proxyReqWs', rewrite)
  }
  return Object.fromEntries(
    DAEMON_ROUTES.map((route) => [route, { target: target.origin, changeOrigin: true, ws: route === '/ws', configure }]),
  )
}
