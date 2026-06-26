<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick } from 'vue'
import { Terminal, Trash2, Info, CheckCircle2, AlertTriangle, XCircle } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import { api } from '../../lib/api'
import { toast } from '../../composables/useToast'
import type { LogEntry, LogLevel } from '../../lib/types'

const logs = ref<LogEntry[]>([])
const listEl = ref<HTMLElement | null>(null)
let lastId = 0
let timer: number | undefined
let stopped = false

const levelIcon: Record<LogLevel, any> = {
  info: Info,
  success: CheckCircle2,
  warn: AlertTriangle,
  error: XCircle,
}

async function poll() {
  if (stopped) return
  try {
    const { logs: entries } = await api.logs.list(lastId)
    if (entries.length) {
      for (const e of entries) if (e.id > lastId) lastId = e.id
      logs.value.push(...entries)
      if (logs.value.length > 300) logs.value = logs.value.slice(-300)
      await nextTick()
      listEl.value?.scrollTo({ top: listEl.value.scrollHeight, behavior: 'smooth' })
    }
  } catch { /* ignore transient */ }
  timer = window.setTimeout(poll, 1500)
}

async function clearLogs() {
  try {
    await api.logs.clear()
    logs.value = []
    lastId = 0
    toast('已清理日志', 'success')
  } catch (e: any) {
    toast(e?.message || '清理失败', 'error')
  }
}

onMounted(poll)
onUnmounted(() => { stopped = true; if (timer) clearTimeout(timer) })
</script>

<template>
  <GlassCard class="console" seam>
    <div class="sec-head mb">
      <div class="sec-title">
        <span class="sec-glyph"><Terminal /></span>
        <div><span class="eyebrow">logs · live</span><h2>实时日志</h2><p>文件检测、直链获取与清理过程</p></div>
      </div>
      <PrimaryButton variant="line" size="sm" @click="clearLogs"><template #icon><Trash2 /></template>清理日志</PrimaryButton>
    </div>
    <div ref="listEl" class="log-list">
      <TransitionGroup name="v-fade">
        <div v-for="log in logs" :key="log.id" class="log" :class="log.level">
          <component :is="levelIcon[log.level]" class="lico" />
          <span class="time">{{ new Date(log.time).toLocaleTimeString('zh-CN', { hour12: false }) }}</span>
          <span class="msg">{{ log.message }}</span>
          <template v-if="log.details?.length">
            <span v-for="(d, i) in log.details" :key="i" class="det">{{ d }}</span>
          </template>
        </div>
      </TransitionGroup>
      <div v-if="!logs.length" class="empty"><Terminal /><p>暂无日志，解析任务运行后这里会实时显示。</p></div>
    </div>
  </GlassCard>
</template>

<style scoped>
.sec-head.mb { margin-bottom: 14px; }
.eyebrow { display: block; margin-bottom: 2px; }
.console { min-height: 100%; display: flex; flex-direction: column; }
.log-list {
  flex: 1 1 auto; min-height: 0; overflow-y: auto;
  border-radius: var(--r-md);
  background: var(--canvas-2);
  border: 1px solid var(--line);
  padding: 7px;
  display: flex; flex-direction: column; gap: 1px;
  font-family: var(--font-mono);
}
.log { display: flex; align-items: flex-start; gap: 8px; padding: 4px 8px; border-radius: var(--r-xs); font-size: var(--fs-xs); line-height: 1.55; color: var(--ink-2); }
.log:hover { background: var(--panel); }
.lico { width: 12px; height: 12px; flex: none; margin-top: 3px; }
.log.info .lico { color: var(--info); }
.log.success .lico { color: var(--ok); }
.log.warn .lico { color: var(--live); }
.log.error .lico { color: var(--danger); }
.log.error { color: var(--danger-ink); }
.time { color: var(--ink-3); flex: none; }
.msg { flex: 1 1 auto; min-width: 0; word-break: break-word; }
.det { color: var(--ink-3); font-size: 10.5px; }
.empty { font-family: var(--font-ui); }

@media (max-width: 720px) {
  .console { min-height: 0; }
  .log-list { flex: none; max-height: 60vh; }
}
</style>
