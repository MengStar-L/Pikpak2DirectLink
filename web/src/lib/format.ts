// Formatting helpers for bytes, durations, and relative time.

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), BYTE_UNITS.length - 1)
  const val = n / Math.pow(1024, i)
  const dig = val >= 100 || i === 0 ? 0 : val >= 10 ? 1 : 2
  return `${val.toFixed(dig)} ${BYTE_UNITS[i]}`
}

export function formatPercent(num: number, den: number): number {
  if (den <= 0) return 0
  return Math.min(100, Math.round((num / den) * 1000) / 10)
}

/** Compact relative time for an ISO timestamp, e.g. "3 分钟前" / "2 天后". */
export function formatRelative(iso: string): string {
  if (!iso) return '-'
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t)) return '-'
  const diff = t - Date.now()
  const abs = Math.abs(diff)
  const past = diff < 0
  const sec = Math.round(abs / 1000)
  if (sec < 60) return past ? `${sec} 秒前` : `${sec} 秒后`
  const min = Math.round(sec / 60)
  if (min < 60) return past ? `${min} 分钟前` : `${min} 分钟后`
  const hr = Math.round(min / 60)
  if (hr < 24) return past ? `${hr} 小时前` : `${hr} 小时后`
  const day = Math.round(hr / 24)
  if (day < 30) return past ? `${day} 天前` : `${day} 天后`
  const mon = Math.round(day / 30)
  if (mon < 12) return past ? `${mon} 个月前` : `${mon} 个月后`
  const yr = Math.round(mon / 12)
  return past ? `${yr} 年前` : `${yr} 年后`
}

export function formatDateTime(iso: string): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '-'
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

export function formatSize(size: string | number): string {
  const n = typeof size === 'string' ? parseInt(size, 10) : size
  return Number.isFinite(n) ? formatBytes(n) : '-'
}
