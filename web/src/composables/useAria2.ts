// aria2 JSON-RPC integration, ported from the legacy aria2.js. The browser
// pushes resolved links straight to the user's own aria2 RPC; nothing about
// the endpoint is sent to this tool's backend. Config persists in localStorage.
import { reactive, ref } from 'vue'
import { toast } from './useToast'

const STORAGE_KEY = 'pikpak.aria2.config'

type Aria2Config = { rpcUrl: string; token: string; dir: string }

function defaults(): Aria2Config {
  return { rpcUrl: 'http://localhost:6800/jsonrpc', token: '', dir: '' }
}

function loadConfig(): Aria2Config {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return defaults()
    return Object.assign(defaults(), JSON.parse(raw))
  } catch {
    return defaults()
  }
}

function saveConfig(cfg: Aria2Config): Aria2Config {
  const clean: Aria2Config = {
    rpcUrl: String(cfg.rpcUrl || '').trim(),
    token: String(cfg.token || '').trim(),
    dir: String(cfg.dir || '').trim(),
  }
  localStorage.setItem(STORAGE_KEY, JSON.stringify(clean))
  return clean
}

function isConfigured(): boolean {
  return Boolean(loadConfig().rpcUrl)
}

// Bare URLs like "http://host:6800" miss the "/jsonrpc" path aria2 serves;
// fill it in so the POST doesn't hit "/" and 404.
function normalizeRpcUrl(raw: string): string {
  const value = String(raw || '').trim()
  if (!value) return ''
  try {
    const u = new URL(value)
    if (u.pathname === '' || u.pathname === '/') u.pathname = '/jsonrpc'
    return u.toString()
  } catch {
    return value
  }
}

async function rpc(method: string, params: any[], cfg?: Aria2Config): Promise<any> {
  cfg = cfg || loadConfig()
  const url = normalizeRpcUrl(cfg.rpcUrl)
  if (!url) throw new Error('请先配置 aria2 RPC 地址')
  const args: any[] = []
  if (cfg.token) args.push('token:' + cfg.token)
  for (const p of params || []) args.push(p)

  let response: Response
  try {
    response = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ jsonrpc: '2.0', id: 'pikpak-' + Date.now(), method, params: args }),
    })
  } catch {
    throw new Error('无法连接 aria2，请检查 RPC 地址、是否开启 RPC 并允许跨域')
  }

  let payload: any = null
  try {
    payload = await response.json()
  } catch {
    payload = null
  }
  if (!response.ok) {
    if (response.status === 404)
      throw new Error('aria2 请求失败 (404)：请确认 RPC 地址指向 /jsonrpc')
    throw new Error(payload?.error?.message || `aria2 请求失败 (${response.status})`)
  }
  if (payload?.error) throw new Error(payload.error.message || 'aria2 返回错误')
  return payload?.result
}

// aria2 writes the file to <dir>/<out>; "out" may carry sub-directories from
// "/" and even "../" out of the dir. Sanitize per-segment: drop ""/"."/"..",
// replace filesystem-reserved chars, trim trailing dots/spaces (Windows).
function sanitizeOutPath(raw: string | null | undefined): string {
  const reserved = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$/i
  const segments = String(raw == null ? '' : raw).replace(/\\/g, '/').split('/')
  const safe: string[] = []
  for (let seg of segments) {
    seg = seg.replace(/[\x00-\x1f<>:"|?*]/g, '_').replace(/[ .]+$/, '').trim()
    if (seg === '' || seg === '.' || seg === '..') continue
    if (reserved.test(seg)) seg = '_' + seg
    safe.push(seg)
  }
  return safe.join('/')
}

function addUri(url: string, name: string, cfg?: Aria2Config) {
  cfg = cfg || loadConfig()
  const options: any = {}
  if (cfg.dir) options.dir = cfg.dir
  const out = sanitizeOutPath(name)
  if (out) options.out = out
  return rpc('aria2.addUri', [[url], options], cfg)
}

// --- config modal state ---
const configOpen = ref(false)
function openConfig() {
  configOpen.value = true
}
function closeConfig() {
  configOpen.value = false
}

// --- push overlay state (for pushMany) ---
const overlay = reactive({
  active: false,
  done: 0,
  total: 0,
  name: '',
})

async function pushOne(url: string, name: string): Promise<boolean> {
  if (!url) {
    toast('链接为空，无法推送', 'error')
    return false
  }
  if (!isConfigured()) {
    toast('请先配置 aria2', 'error')
    openConfig()
    return false
  }
  try {
    await addUri(url, name)
    toast('已推送到 aria2' + (name ? '：' + name : ''), 'success')
    return true
  } catch (e: any) {
    toast(e?.message || '推送失败', 'error')
    return false
  }
}

let pushManyBusy = false
async function pushMany(items: { url: string; name: string }[]) {
  const list = (items || []).filter((it) => it && it.url)
  if (!list.length) {
    toast('没有可推送的链接', 'error')
    return
  }
  if (!isConfigured()) {
    toast('请先配置 aria2', 'error')
    openConfig()
    return
  }
  if (pushManyBusy) return
  pushManyBusy = true
  overlay.active = true
  overlay.done = 0
  overlay.total = list.length
  overlay.name = ''
  let ok = 0
  const failures: string[] = []
  try {
    for (let i = 0; i < list.length; i++) {
      const it = list[i]
      overlay.name = it.name || ''
      try {
        await addUri(it.url, it.name)
        ok += 1
      } catch (e: any) {
        failures.push(e?.message || '未知错误')
      }
      overlay.done = i + 1
    }
  } finally {
    await new Promise((r) => setTimeout(r, 450))
    overlay.active = false
    pushManyBusy = false
  }
  if (!failures.length) toast(`已推送 ${ok} 个链接到 aria2`, 'success')
  else if (ok > 0) toast(`已推送 ${ok} 个，${failures.length} 个失败`, 'info')
  else toast('推送失败：' + (failures[0] || '未知错误'), 'error')
}

export const aria2 = {
  loadConfig,
  saveConfig,
  isConfigured,
  openConfig,
  closeConfig,
  configOpen,
  overlay,
  rpc,
  addUri,
  pushOne,
  pushMany,
}

export type { Aria2Config }
