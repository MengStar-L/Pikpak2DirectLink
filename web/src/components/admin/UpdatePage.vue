<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Download, RefreshCw, ExternalLink, Tag, Cpu, Clock, CheckCircle2, ArrowDownToLine } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import { api } from '../../lib/api'
import { toast } from '../../composables/useToast'
import { formatRelative, formatBytes, formatPercent } from '../../lib/format'
import type { UpdateStatus } from '../../lib/types'

const emit = defineEmits<{ (e: 'available', value: boolean): void }>()

const status = ref<UpdateStatus | null>(null)
const loading = ref(true)
const actionLoading = ref(false)
let timer: number | undefined
let stopped = false

const ACTIVE: UpdateStatus['phase'][] = ['checking', 'downloading', 'verifying', 'installing', 'restarting']

const PHASE_LABEL: Record<string, string> = {
  idle: '空闲',
  checking: '检查中',
  up_to_date: '已是最新',
  available: '有新版本',
  downloading: '下载中',
  verifying: '校验中',
  installing: '安装中',
  restarting: '重启中',
  error: '出错',
}
const phaseLabel = computed(() => PHASE_LABEL[status.value?.phase || 'idle'] || status.value?.phase || '空闲')
const isActive = computed(() => ACTIVE.includes(status.value?.phase || 'idle'))

async function load() {
  try {
    status.value = await api.update.status()
    emit('available', Boolean(status.value.update_available))
  } catch (e: any) {
    toast(e?.message || '加载更新状态失败', 'error')
  } finally {
    loading.value = false
  }
  if (status.value && ACTIVE.includes(status.value.phase) && !stopped) {
    timer = window.setTimeout(load, 1500)
  }
}

async function check() {
  actionLoading.value = true
  try {
    status.value = await api.update.check()
    emit('available', Boolean(status.value.update_available))
    if (status.value.update_available) toast(`发现新版本 ${status.value.latest_version}`, 'info')
    else toast('已是最新版本', 'success')
    if (ACTIVE.includes(status.value.phase)) load()
  } catch (e: any) {
    toast(e?.message || '检查失败', 'error')
  } finally {
    actionLoading.value = false
  }
}

async function install() {
  actionLoading.value = true
  try {
    await api.update.install()
    toast('开始下载更新…', 'success')
    load()
  } catch (e: any) {
    toast(e?.message || '安装失败', 'error')
  } finally {
    actionLoading.value = false
  }
}

const progressPct = computed(() => {
  if (!status.value) return 0
  if (status.value.progress) return Math.round(status.value.progress)
  if (status.value.total_bytes) return formatPercent(status.value.downloaded_bytes, status.value.total_bytes)
  return status.value.phase === 'verifying' ? 100 : 0
})

onMounted(load)
onUnmounted(() => { stopped = true; if (timer) clearTimeout(timer) })
</script>

<template>
  <GlassCard seam>
    <div class="sec-head mb">
      <div class="sec-title">
        <span class="sec-glyph"><Download /></span>
        <div><span class="eyebrow">version</span><h2>版本与更新</h2><p>检测 GitHub 对应架构的新版本并下载安装</p></div>
      </div>
      <span class="pill" :class="status?.update_available ? 'pill-live' : isActive ? 'pill-info' : 'pill-ok'">
        <span class="dot" :class="{ live: isActive }" />{{ phaseLabel }}
      </span>
    </div>

    <dl class="grid">
      <div><dt><Tag />当前版本</dt><dd class="mono">{{ status?.current_version || '-' }}</dd></div>
      <div><dt><Tag />最新版本</dt><dd class="mono">{{ status?.latest_version || '-' }}</dd></div>
      <div><dt><Cpu />运行平台</dt><dd class="mono">{{ status?.platform || '-' }}</dd></div>
      <div><dt><Clock />上次检查</dt><dd class="mono">{{ status?.checked_at ? formatRelative(status.checked_at) : '-' }}</dd></div>
    </dl>

    <Transition name="v-fade">
      <div v-if="status && isActive" class="progress">
        <div class="progress-head">
          <span class="eyebrow">{{ phaseLabel }}</span>
          <span class="mono">{{ progressPct }}%</span>
        </div>
        <div class="bar"><div class="bar-fill live" :style="{ width: progressPct + '%' }" /></div>
        <div v-if="status.total_bytes" class="progress-meta mono">{{ formatBytes(status.downloaded_bytes) }} / {{ formatBytes(status.total_bytes) }}</div>
      </div>
    </Transition>

    <Transition name="v-fade"><p v-if="status?.error" class="error-block">{{ status.error }}</p></Transition>

    <div class="actions">
      <PrimaryButton variant="line" :loading="actionLoading && status?.phase === 'checking'" :disabled="actionLoading" @click="check">
        <template #icon><RefreshCw /></template>检查更新
      </PrimaryButton>
      <PrimaryButton :disabled="!status?.update_available || actionLoading || !status?.managed" @click="install">
        <template #icon><ArrowDownToLine /></template>立即更新
      </PrimaryButton>
      <a v-if="status?.release_url" class="link-btn" :href="status.release_url" target="_blank" rel="noreferrer noopener"><ExternalLink />发布页</a>
    </div>

    <Transition name="v-fade">
      <div v-if="status?.release_notes" class="notes">
        <h3><CheckCircle2 />更新内容</h3>
        <pre class="notes-body mono">{{ status.release_notes }}</pre>
      </div>
    </Transition>
  </GlassCard>
</template>

<style scoped>
.sec-head.mb { margin-bottom: 16px; }
.eyebrow { display: block; margin-bottom: 2px; }

.grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; margin: 0 0 14px; }
.grid > div { padding: 10px 12px; border-radius: var(--r-md); background: var(--panel-2); border: 1px solid var(--line); }
.grid dt { display: flex; align-items: center; gap: 6px; font-size: var(--fs-2xs); color: var(--ink-3); font-weight: var(--fw-med); margin-bottom: 4px; }
.grid dt svg { width: 12px; height: 12px; }
.grid dd { margin: 0; font-size: var(--fs-md); font-weight: var(--fw-semi); color: var(--ink); }

.progress { margin-bottom: 14px; display: flex; flex-direction: column; gap: 6px; }
.progress-head { display: flex; justify-content: space-between; align-items: center; font-size: var(--fs-xs); color: var(--ink-2); }
.progress-meta { font-size: var(--fs-xs); color: var(--ink-3); }

.actions { display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }

.notes { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--line); }
.notes h3 { display: flex; align-items: center; gap: 6px; font-size: var(--fs-md); margin-bottom: 8px; }
.notes h3 svg { width: 15px; height: 15px; color: var(--ok); }
.notes-body { margin: 0; padding: 11px; border-radius: var(--r-md); background: var(--panel-2); border: 1px solid var(--line); font-size: var(--fs-xs); color: var(--ink-2); white-space: pre-wrap; word-break: break-word; max-height: 260px; overflow-y: auto; line-height: 1.6; }

@media (max-width: 560px) { .grid { grid-template-columns: 1fr; } }
</style>
