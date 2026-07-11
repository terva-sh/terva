export function humanBytes(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB'
  return n + ' B'
}

export function humanCount(n: number): string {
  if (n >= 1_000_000) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1e3).toFixed(0) + 'k'
  return String(n)
}

export function compact(v: unknown): string {
  try {
    return truncate(JSON.stringify(v), 200)
  } catch {
    return ''
  }
}

export function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s
}
