// The Talkoot Avatar Studio's Save (studio.html). A Vite plugin that runs in
// the dev server only: POST /__studio/save writes the face's pose table,
// src/features/talkoot/face/poses.json, so a pose tuned in the studio becomes
// an ordinary diff to commit.
//
// It writes that one file and no other. A request must come from the page the
// dev server serves: its Origin, when it sends one, names this server, and its
// body is application/json, which a cross-site form cannot send without a
// preflight this route never answers. The table must have the shape the
// renderer reads (validTable), and the file is written in one rename. Vite's
// own host check (server.allowedHosts) refuses a rebound DNS name before any
// of this runs.

import { rename, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import type { Plugin } from 'vite'
import { formatTable, validTable } from '../src/studio/table'
import type { Table } from '../src/features/talkoot/face/poses'

export const SAVE_ROUTE = '/__studio/save'
export const TABLE_PATH = fileURLToPath(new URL('../src/features/talkoot/face/poses.json', import.meta.url))

const LIMIT = 1 << 20

// Req and Res are the parts of Node's request and response that handle uses,
// so a test can pass its own.
export interface Req extends AsyncIterable<Uint8Array | string> {
  method?: string
  headers: Record<string, string | string[] | undefined>
}
export interface Res {
  statusCode: number
  setHeader(name: string, value: string): unknown
  end(body: string): unknown
}

async function writeTable(text: string) {
  const tmp = `${TABLE_PATH}.studio-${process.pid}`
  await writeFile(tmp, text)
  await rename(tmp, TABLE_PATH)
}

function reply(res: Res, status: number, message: string) {
  res.statusCode = status
  res.setHeader('content-type', 'text/plain; charset=utf-8')
  res.end(message)
}

const header = (req: Req, name: string): string | undefined => {
  const v = req.headers[name]
  return Array.isArray(v) ? v[0] : v
}

// sameOrigin is whether a request came from a page on this server. A browser
// always sends Origin on a POST, so an absent one is a tool such as curl on
// this machine.
function sameOrigin(req: Req): boolean {
  const origin = header(req, 'origin')
  if (origin === undefined) return true
  try {
    return new URL(origin).host === header(req, 'host')
  } catch {
    return false
  }
}

// handle answers one request. write is the file write, which a test replaces.
// A GET answers with the path a POST writes, so a test can confirm that the
// dev server it reached belongs to its own checkout before it saves.
export async function handle(req: Req, res: Res, write: (text: string) => Promise<void> = writeTable): Promise<void> {
  if (req.method === 'GET') return reply(res, 200, TABLE_PATH)
  if (req.method !== 'POST') return reply(res, 405, 'POST the pose table')
  if (!sameOrigin(req)) return reply(res, 403, 'the studio saves from its own page only')
  if (!/^application\/json\b/i.test(header(req, 'content-type') ?? '')) return reply(res, 415, 'send application/json')
  const chunks: Uint8Array[] = []
  let size = 0
  try {
    for await (const chunk of req) {
      const bytes = typeof chunk === 'string' ? new TextEncoder().encode(chunk) : chunk
      size += bytes.length
      if (size > LIMIT) return reply(res, 413, 'the table is larger than 1 MiB')
      chunks.push(bytes)
    }
  } catch {
    return reply(res, 400, 'the request body did not arrive')
  }
  const all = new Uint8Array(size)
  let at = 0
  for (const c of chunks) {
    all.set(c, at)
    at += c.length
  }
  let table: unknown
  try {
    table = JSON.parse(new TextDecoder().decode(all))
  } catch (e) {
    return reply(res, 400, `not JSON: ${(e as Error).message}`)
  }
  const bad = validTable(table)
  if (bad) return reply(res, 422, bad)
  try {
    await write(formatTable(table as Table))
  } catch (e) {
    return reply(res, 500, `the write failed: ${(e as Error).message}`)
  }
  return reply(res, 200, 'saved')
}

// studioSave is the plugin. apply: 'serve' keeps it out of every build.
export function studioSave(): Plugin {
  return {
    name: 'terva-studio-save',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use(SAVE_ROUTE, (req, res) => {
        void handle(req as Req, res)
      })
    },
  }
}
