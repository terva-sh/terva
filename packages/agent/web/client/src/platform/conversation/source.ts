import { t } from '../../i18n'

// The writer of a text that terva queued, as a person reads it. The wire calls
// this the source (WireMessage.source, Snapshot.queued_sources): a short word
// such as "swarm:recap". A value with no name here shows as it is, so a new
// source is visible before anybody names it.
export function sourceLabel(source: string): string {
  switch (source) {
    case 'swarm:recap':
      return t('sub-agent report')
    case 'talkoot:delivery':
      return t('team message')
  }
  return source
}

// The queue without the entry at one position: its text and its source leave
// together, so the two lists stay the same length.
export function withoutEntry(
  q: { texts: string[]; sources: string[] },
  at: number,
): { texts: string[]; sources: string[] } {
  return {
    texts: q.texts.filter((_, i) => i !== at),
    sources: q.sources.filter((_, i) => i !== at),
  }
}

// The sources that belong to a queue view. A list of another length is not
// this queue's list, so every entry then reads as typed, which is how the
// queue of an older daemon reads.
export function queueSources(texts: string[] | undefined, sources: string[] | undefined): string[] {
  const n = texts?.length ?? 0
  return sources && sources.length === n ? sources : Array.from({ length: n }, () => '')
}
