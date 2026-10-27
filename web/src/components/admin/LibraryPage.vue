<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { AlertTriangle, ChevronRight, Copy, File, Folder, FolderInput, FolderPlus, Home, Inbox, Link2, Loader2, MoreHorizontal, Pencil, Plus, RefreshCw, Trash2, X } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import { api } from '../../lib/api'
import { copyText } from '../../lib/clipboard'
import { formatSize } from '../../lib/format'
import { toast } from '../../composables/useToast'
import type { LibraryEntry, LibraryFile } from '../../lib/types'

type ListedFile = LibraryFile & { public_url?: string; mode?: 'direct' | 'proxy' }
const entries = ref<LibraryEntry[]>([])
const folderID = ref('')
const selectedID = ref('')
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const dialog = ref<'' | 'add' | 'folder' | 'rename' | 'move' | 'delete'>('')
const targetID = ref('')
const draft = ref({ name: '', input: '', pass_code: '', parent_id: '' })
const formError = ref('')
const menu = ref<{ id: string; x: number; y: number } | null>(null)
const menuElement = ref<HTMLElement | null>(null)
const linkFileID = ref('')
const linkMode = ref<'direct' | 'proxy'>('direct')
const linkURL = ref('')
const linkError = ref('')
const linkBusy = ref(false)
let alive = true
let requestVersion = 0
let timer: number | undefined

const byID = computed(() => new Map(entries.value.map(e => [e.id, e])))
const currentEntries = computed(() => entries.value.filter(e => e.parent_id === folderID.value).sort((a, b) => Number(b.kind === 'folder') - Number(a.kind === 'folder') || a.name.localeCompare(b.name, 'zh-CN')))
const selected = computed(() => byID.value.get(selectedID.value))
const target = computed(() => byID.value.get(targetID.value))
const menuTarget = computed(() => menu.value ? byID.value.get(menu.value.id) : undefined)
const files = computed(() => [...(selected.value?.files || [])].sort((a, b) => (a.path || a.name).localeCompare(b.path || b.name, 'zh-CN')) as ListedFile[])
const activeFile = computed(() => files.value.find(f => f.id === linkFileID.value))
const breadcrumbs = computed(() => {
  const list: LibraryEntry[] = []
  const seen = new Set<string>()
  let current = byID.value.get(folderID.value)
  while (current && !seen.has(current.id)) { seen.add(current.id); list.unshift(current); current = byID.value.get(current.parent_id) }
  return list
})
function isDescendant(id: string, ancestor: string): boolean {
  const seen = new Set<string>()
  let current = byID.value.get(id)
  while (current && !seen.has(current.id)) { if (current.id === ancestor) return true; seen.add(current.id); current = byID.value.get(current.parent_id) }
  return false
}
function folderPath(id: string): string {
  const parts: string[] = []
  const seen = new Set<string>()
  let current = byID.value.get(id)
  while (current && !seen.has(current.id)) { seen.add(current.id); parts.unshift(current.name); current = byID.value.get(current.parent_id) }
  return '/' + parts.join('/')
}
const destinations = computed(() => entries.value.filter(e => e.kind === 'folder' && !isDescendant(e.id, targetID.value)).sort((a, b) => folderPath(a.id).localeCompare(folderPath(b.id), 'zh-CN')))
function schedulePoll() {
  window.clearTimeout(timer)
  if (alive && entries.value.some(e => e.status === 'pending')) timer = window.setTimeout(() => void load(false), 2000)
}
async function load(show = true) {
  const version = ++requestVersion
  if (show) loading.value = true
  try {
    const result = await api.library.list()
    if (!alive || version !== requestVersion) return
    entries.value = result.entries || []
    error.value = ''
    if (folderID.value && !byID.value.has(folderID.value)) folderID.value = ''
    if (selectedID.value && !byID.value.has(selectedID.value)) { selectedID.value = ''; linkFileID.value = '' }
  } catch (e: any) { if (alive && version === requestVersion) error.value = e?.message || '读取文件库失败' }
  finally { if (alive && version === requestVersion) { loading.value = false; schedulePoll() } }
}
function navigate(id: string) { folderID.value = id; selectedID.value = ''; menu.value = null }
function openEntry(entry: LibraryEntry) { menu.value = null; if (entry.kind === 'folder') navigate(entry.id); else selectedID.value = entry.id }
async function showMenu(event: MouseEvent, id: string) {
  event.preventDefault(); event.stopPropagation()
  menu.value = { id, x: Math.max(8, Math.min(event.clientX, window.innerWidth - 192)), y: Math.max(8, Math.min(event.clientY, window.innerHeight - 240)) }
  await nextTick(); menuElement.value?.querySelector<HTMLButtonElement>('button')?.focus()
}
function openDialog(kind: typeof dialog.value, entry?: LibraryEntry) {
  menu.value = null; targetID.value = entry?.id || ''; formError.value = ''
  draft.value = { name: entry?.name || '', input: '', pass_code: '', parent_id: entry?.parent_id || folderID.value }
  dialog.value = kind
}
function closeDialog() { if (!busy.value) dialog.value = '' }
async function submit() {
  if (busy.value) return
  formError.value = ''; busy.value = true
  try {
    const kind = dialog.value
    if ((kind === 'folder' || kind === 'rename') && !draft.value.name.trim()) throw new Error('请填写名称')
    if (kind === 'add' && !draft.value.input.trim()) throw new Error('请填写磁力链接或 PikPak 分享链接')
    if (kind === 'add' || kind === 'folder') await api.library.create({ kind: kind === 'folder' ? 'folder' : 'resource', parent_id: folderID.value, name: draft.value.name.trim(), input: kind === 'add' ? draft.value.input.trim() : undefined, pass_code: draft.value.pass_code.trim() })
    else if (kind === 'rename') await api.library.update(targetID.value, { name: draft.value.name.trim() })
    else if (kind === 'move') await api.library.update(targetID.value, { parent_id: draft.value.parent_id })
    else if (kind === 'delete') await api.library.remove(targetID.value)
    dialog.value = ''; toast(kind === 'delete' ? '已删除，相关下载链接已失效' : '已保存', 'success'); await load(false)
  } catch (e: any) { formError.value = e?.message || '操作失败' }
  finally { busy.value = false }
}
async function retry(entry: LibraryEntry) {
  menu.value = null
  if (busy.value) return
  busy.value = true
  try { await api.library.retry(entry.id); toast('已重新开始读取文件', 'success'); await load(false) }
  catch (e: any) { error.value = e?.message || '重试失败'; toast(error.value, 'error') }
  finally { busy.value = false }
}
function openLink(file: ListedFile) { linkFileID.value = file.id; linkMode.value = file.mode || 'direct'; linkURL.value = file.public_url || ''; linkError.value = '' }
function closeLink() { if (!linkBusy.value) { linkFileID.value = ''; linkURL.value = ''; linkError.value = '' } }
async function generate(reset = false) {
  if (!selected.value || !activeFile.value || linkBusy.value) return
  linkBusy.value = true; linkError.value = ''
  try { const result = await api.library.link(selected.value.id, activeFile.value.id, linkMode.value, reset); linkURL.value = result.url; linkMode.value = result.mode; await load(false); toast(reset ? '已重置链接，旧链接已失效' : '下载链接已生成', 'success') }
  catch (e: any) { linkError.value = e?.message || '生成链接失败' }
  finally { linkBusy.value = false }
}
async function copyLink() { if (await copyText(linkURL.value)) toast('下载链接已复制', 'success'); else { linkError.value = '复制失败，请手动选中链接复制'; toast(linkError.value, 'error') } }
async function revoke() {
  if (!selected.value || !activeFile.value || linkBusy.value) return
  linkBusy.value = true; linkError.value = ''
  try { await api.library.revokeLink(selected.value.id, activeFile.value.id); linkURL.value = ''; await load(false); toast('已停用下载链接', 'success') }
  catch (e: any) { linkError.value = e?.message || '停用失败' }
  finally { linkBusy.value = false }
}
function onKey(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  if (menu.value) menu.value = null
  else if (linkFileID.value) closeLink()
  else if (dialog.value) closeDialog()
  else selectedID.value = ''
}
function statusText(entry: LibraryEntry) { return entry.kind === 'folder' ? `${entries.value.filter(e => e.parent_id === entry.id).length} 个项目` : entry.status === 'pending' ? '正在读取文件…' : entry.status === 'failed' ? '读取失败' : `${entry.file_count} 个文件` }
function fileDirectory(file: LibraryFile) { const path = file.path?.replace(/\\/g, '/') || ''; const i = path.lastIndexOf('/'); return i > 0 ? path.slice(0, i) : '' }
onMounted(() => { void load(); window.addEventListener('keydown', onKey) })
onUnmounted(() => { alive = false; ++requestVersion; window.clearTimeout(timer); window.removeEventListener('keydown', onKey) })
</script>

<template>
  <div class="library-page">
    <GlassCard seam>
      <div class="library-head">
        <div class="sec-title"><span class="sec-glyph"><Folder /></span><div><span class="eyebrow">library</span><h2>文件库</h2><p>整理种子与分享链接，为文件生成固定下载入口</p></div></div>
        <div class="actions"><PrimaryButton variant="line" @click="openDialog('folder')"><FolderPlus />新建文件夹</PrimaryButton><PrimaryButton @click="openDialog('add')"><Plus />添加资源</PrimaryButton></div>
      </div>
      <div class="toolbar"><nav class="breadcrumbs" aria-label="文件夹路径"><button type="button" @click="navigate('')"><Home />全部文件</button><template v-for="crumb in breadcrumbs" :key="crumb.id"><ChevronRight /><button type="button" @click="navigate(crumb.id)">{{ crumb.name }}</button></template></nav><button class="btn btn-ghost btn-sm" type="button" :disabled="loading" @click="load()"><RefreshCw :class="{ spin: loading }" />刷新</button></div>
      <p class="hint">右键项目或点击「更多」可移动、重命名和删除。多文件资源可展开查看每个文件。</p>
      <p v-if="error" class="error-block" role="alert">{{ error }}</p>
      <div v-if="loading && !entries.length" class="empty"><Loader2 class="spin" /><p>正在读取文件库…</p></div>
      <div v-else-if="!currentEntries.length" class="empty"><Inbox /><p>此文件夹为空，添加一个磁力链接或 PikPak 分享链接开始。</p></div>
      <div v-else class="entry-list">
        <div v-for="entry in currentEntries" :key="entry.id" class="entry-row" @contextmenu="showMenu($event, entry.id)">
          <button class="entry-open" type="button" @click="openEntry(entry)"><span class="entry-icon" :class="{ folder: entry.kind === 'folder' }"><Folder v-if="entry.kind === 'folder'" /><Loader2 v-else-if="entry.status === 'pending'" class="spin" /><AlertTriangle v-else-if="entry.status === 'failed'" /><File v-else /></span><span class="entry-text"><strong>{{ entry.name }}</strong><span>{{ entry.kind === 'resource' ? (entry.resource_kind === 'magnet' ? '磁力链接 · ' : 'PikPak 分享 · ') : '' }}{{ statusText(entry) }}</span><span v-if="entry.error" class="entry-error">{{ entry.error }}</span></span></button>
          <button type="button" class="btn btn-ghost btn-icon" :aria-label="`${entry.name} 的更多操作`" @click="showMenu($event, entry.id)"><MoreHorizontal /></button>
        </div>
      </div>
    </GlassCard>

    <Teleport to="body">
      <template v-if="menu && menuTarget"><div class="library-menu-cover" @pointerdown="menu = null" /><div ref="menuElement" class="library-context panel" role="menu" :style="{ left: `${menu.x}px`, top: `${menu.y}px` }"><button role="menuitem" @click="openEntry(menuTarget)"><Folder />打开</button><button role="menuitem" @click="openDialog('rename', menuTarget)"><Pencil />重命名</button><button role="menuitem" @click="openDialog('move', menuTarget)"><FolderInput />移动到…</button><button v-if="menuTarget.status === 'failed' && menuTarget.kind === 'resource'" role="menuitem" :disabled="busy" @click="retry(menuTarget)"><RefreshCw />重新读取</button><button class="danger" role="menuitem" @click="openDialog('delete', menuTarget)"><Trash2 />删除</button></div></template>

      <div v-if="dialog" class="modal-overlay library-overlay" @click.self="closeDialog"><section class="panel library-dialog" role="dialog" aria-modal="true" aria-labelledby="library-dialog-title"><div class="dialog-head"><h3 id="library-dialog-title">{{ { add: '添加资源', folder: '新建文件夹', rename: '重命名', move: '移动项目', delete: '删除项目' }[dialog] }}</h3><button class="btn btn-ghost btn-icon" type="button" :disabled="busy" aria-label="关闭" @click="closeDialog"><X /></button></div><form @submit.prevent="submit">
        <template v-if="dialog === 'add'"><label class="field"><span class="field-label">磁力链接 / PikPak 分享链接</span><textarea v-model="draft.input" class="input source-input" rows="4" required placeholder="magnet:?xt=urn:btih:… 或 https://mypikpak.com/s/…" /></label><label class="field"><span class="field-label">名称（可选）</span><input v-model="draft.name" class="input" maxlength="255" placeholder="留空则自动识别" /></label><label class="field"><span class="field-label">分享提取码（可选）</span><input v-model="draft.pass_code" class="input" autocomplete="off" /></label><p class="hint">添加后自动读取文件列表。文件数量较多时需要等待云端处理。</p></template>
        <label v-else-if="dialog === 'folder' || dialog === 'rename'" class="field"><span class="field-label">名称</span><input v-model="draft.name" class="input" required maxlength="255" /></label>
        <template v-else-if="dialog === 'move'"><p class="dialog-description">将「{{ target?.name }}」移动到：</p><label class="field"><span class="field-label">目标文件夹</span><select v-model="draft.parent_id" class="input"><option value="">/ 全部文件</option><option v-for="destination in destinations" :key="destination.id" :value="destination.id">{{ folderPath(destination.id) }}</option></select></label></template>
        <p v-else class="dialog-description">确定删除「{{ target?.name }}」{{ target?.kind === 'folder' ? '及其全部子文件夹和资源' : '' }}？相关对外下载链接将立即失效。</p>
        <p v-if="formError" class="error-block" role="alert">{{ formError }}</p><div class="dialog-actions"><PrimaryButton variant="line" :disabled="busy" @click="closeDialog">取消</PrimaryButton><PrimaryButton type="submit" :variant="dialog === 'delete' ? 'danger' : 'primary'" :loading="busy">{{ dialog === 'delete' ? '确认删除' : '保存' }}</PrimaryButton></div>
      </form></section></div>

      <div v-if="selected" class="modal-overlay library-overlay" @click.self="selectedID = ''"><section class="panel library-dialog files-dialog" role="dialog" aria-modal="true" aria-labelledby="library-files-title"><div class="dialog-head"><div><h3 id="library-files-title">{{ selected.name }}</h3><p class="hint">{{ statusText(selected) }}</p></div><button class="btn btn-ghost btn-icon" type="button" aria-label="关闭文件列表" @click="selectedID = ''"><X /></button></div><p v-if="selected.error" class="error-block">{{ selected.error }}</p><div v-if="selected.status === 'pending'" class="empty"><Loader2 class="spin" /><p>正在读取文件，完成后自动显示。</p></div><div v-else-if="!files.length" class="empty"><Inbox /><p>暂无可下载文件</p><PrimaryButton v-if="selected.status === 'failed'" :loading="busy" @click="retry(selected)">重新读取</PrimaryButton></div><div v-else class="files-list"><div v-for="(file, index) in files" :key="file.id" class="file-group"><div v-if="fileDirectory(file) && (index === 0 || fileDirectory(files[index - 1]!) !== fileDirectory(file))" class="file-directory"><Folder />{{ fileDirectory(file) }}</div><div class="file-row"><File /><span class="file-name"><strong>{{ file.name }}</strong><small>{{ formatSize(file.size) }}<span v-if="file.public_url"> · 已生成{{ file.mode === 'proxy' ? '中转' : '直链' }}入口</span></small></span><PrimaryButton variant="line" size="sm" @click="openLink(file)"><Link2 />下载链接</PrimaryButton></div></div></div></section></div>

      <div v-if="linkFileID && activeFile" class="modal-overlay library-overlay link-overlay" @click.self="closeLink"><section class="panel library-dialog" role="dialog" aria-modal="true" aria-labelledby="library-link-title"><div class="dialog-head"><h3 id="library-link-title">对外下载链接</h3><button type="button" class="btn btn-ghost btn-icon" :disabled="linkBusy" aria-label="关闭链接设置" @click="closeLink"><X /></button></div><p class="dialog-description filename">{{ activeFile.name }}</p><p class="hint">外部程序无需登录。访问固定链接时自动解析此文件，并跳转至直连或中转下载地址。请将链接交给需要下载的人。</p><label class="field"><span class="field-label">下载方式</span><select v-model="linkMode" class="input" :disabled="linkBusy"><option value="direct">PikPak 直连下载</option><option value="proxy">经本程序中转下载</option></select></label><PrimaryButton :loading="linkBusy" @click="generate()">{{ linkURL ? '保存下载方式' : '生成下载链接' }}</PrimaryButton><template v-if="linkURL"><label class="field link-field"><span class="field-label">固定下载链接</span><input class="input input-mono" :value="linkURL" readonly aria-label="固定下载链接" @focus="($event.target as HTMLInputElement).select()" /></label><div class="actions"><PrimaryButton variant="soft" :disabled="linkBusy" @click="copyLink"><Copy />复制链接</PrimaryButton><PrimaryButton variant="line" :disabled="linkBusy" @click="generate(true)">重置链接</PrimaryButton><PrimaryButton variant="danger" :disabled="linkBusy" @click="revoke">停用链接</PrimaryButton></div><p class="hint">重置会使旧链接失效；停用后外部无法通过此入口下载。</p></template><p v-if="linkError" class="error-block" role="alert">{{ linkError }}</p></section></div>
    </Teleport>
  </div>
</template>

<style scoped>
.library-page { display: flex; flex-direction: column; gap: 14px; }
.library-head,.toolbar,.actions,.dialog-head,.dialog-actions { display: flex; align-items: center; gap: 10px; }
.library-head,.toolbar,.dialog-head { justify-content: space-between; }
.library-head { flex-wrap: wrap; margin-bottom: 18px; }
.eyebrow { display: block; margin-bottom: 2px; }
.actions { flex-wrap: wrap; }
.toolbar { gap: 8px; padding: 10px 0; border-top: 1px solid var(--line); }
.breadcrumbs { display: flex; align-items: center; gap: 5px; flex-wrap: wrap; min-width: 0; }
.breadcrumbs button { display: flex; align-items: center; gap: 5px; color: var(--ink-2); font-size: var(--fs-xs); overflow-wrap: anywhere; }
.breadcrumbs button:hover { color: var(--brand); }
.breadcrumbs svg { width: 14px; height: 14px; flex: none; }
.hint { color: var(--ink-3); font-size: var(--fs-xs); line-height: 1.7; margin: 7px 0; }
.entry-list { margin-top: 14px; border-top: 1px solid var(--line); }
.entry-row { display: flex; align-items: center; gap: 10px; border-bottom: 1px solid var(--line); padding: 10px 2px; }
.entry-row:hover { background: var(--panel-2); }
.entry-open { display: flex; align-items: center; gap: 12px; min-width: 0; flex: 1; text-align: left; }
.entry-icon { flex: none; width: 38px; height: 38px; border-radius: var(--r-sm); background: var(--panel-2); display: grid; place-items: center; color: var(--ink-3); }
.entry-icon.folder { color: var(--brand); background: var(--brand-soft); }
.entry-icon svg { width: 21px; height: 21px; }
.entry-text { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.entry-text strong,.file-name strong { font-size: var(--fs-sm); font-weight: var(--fw-med); overflow-wrap: anywhere; }
.entry-text > span { color: var(--ink-3); font-size: var(--fs-xs); }
.entry-text .entry-error { color: var(--danger); }
.library-menu-cover { position: fixed; inset: 0; z-index: 9100; }
.library-context { position: fixed; z-index: 9101; width: 184px; padding: 5px; box-shadow: var(--shadow-md); border-radius: var(--r-md); }
.library-context button { width: 100%; display: flex; gap: 10px; align-items: center; padding: 9px 10px; font-size: var(--fs-xs); text-align: left; border-radius: var(--r-sm); }
.library-context button:hover,.library-context button:focus-visible { background: var(--panel-2); }
.library-context svg { width: 15px; height: 15px; }
.danger { color: var(--danger); }
.library-overlay { z-index: 9000; position: fixed; inset: 0; background: rgb(0 0 0 / .4); backdrop-filter: blur(4px); padding: 20px; display: flex; align-items: center; justify-content: center; }
.library-dialog { width: min(530px, 100%); padding: 22px; max-height: calc(100dvh - 40px); overflow-y: auto; border-radius: var(--r-lg); box-shadow: var(--shadow-md); }
.dialog-head { margin-bottom: 14px; align-items: flex-start; }
.dialog-head h3 { font-size: var(--fs-md); overflow-wrap: anywhere; }
.dialog-head > div { min-width: 0; }
.library-dialog .field { margin: 12px 0; }
.source-input { resize: vertical; min-height: 100px; font-family: var(--font-mono); line-height: 1.7; }
.dialog-description { color: var(--ink-2); font-size: var(--fs-sm); line-height: 1.8; }
.dialog-actions { justify-content: flex-end; margin-top: 20px; }
.files-dialog { width: min(780px, 100%); }
.files-list { display: flex; flex-direction: column; }
.file-directory { display: flex; align-items: center; gap: 8px; padding: 12px 0 5px; color: var(--ink-3); font-size: var(--fs-xs); overflow-wrap: anywhere; }
.file-directory svg { width: 15px; height: 15px; flex: none; }
.file-row { display: flex; align-items: center; gap: 10px; padding: 12px 0 12px 10px; border-bottom: 1px solid var(--line); }
.file-row > svg { color: var(--ink-3); width: 18px; height: 18px; flex: none; }
.file-name { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.file-name small { color: var(--ink-3); font-size: var(--fs-xs); }
.file-row .btn { flex: none; }
.link-overlay { z-index: 9200; }
.filename { overflow-wrap: anywhere; }
.link-field { margin-top: 20px !important; }
@media (max-width: 540px) { .library-head .actions { width: 100%; } .library-head .actions .btn { flex: 1; } .library-overlay { padding: 12px; } .library-dialog { padding: 16px; max-height: calc(100dvh - 24px); } .file-row { padding-left: 0; gap: 8px; } .file-row > svg { display: none; } .file-row .btn { padding: 6px 8px; } }
</style>
