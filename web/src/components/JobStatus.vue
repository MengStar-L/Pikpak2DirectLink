<script setup lang="ts">
import { computed } from 'vue'
import { CheckCircle2, XCircle, AlertTriangle, Users, Loader2 } from 'lucide-vue-next'
import SignalTrack from './SignalTrack.vue'
import type { Job, UserJobView } from '../lib/types'

const props = defineProps<{
  job: Job | UserJobView | null
  phase: string
  error: string
  submitting?: boolean
  showAttempts?: boolean
}>()

const isBatch = computed(() => props.job?.kind === 'batch')
const batch = computed(() => (props.job?.kind === 'batch' ? props.job?.batch : null))
const batchPct = computed(() => {
  const b = batch.value
  if (!b || b.total === 0) return 0
  return Math.round(((b.succeeded + b.failed) / b.total) * 100)
})
const attempts = computed(() =>
  props.showAttempts ? ((props.job as Job | null)?.account_attempts ?? []) : [],
)
</script>

<template>
  <div class="dock">
    <SignalTrack :phase="phase" :stage="job?.stage" />

    <Transition name="v-fade">
      <p v-if="job?.message" class="message">{{ job.message }}</p>
    </Transition>
    <Transition name="v-fade">
      <p v-if="job?.queue_ahead && phase === 'queued'" class="queue mono">前面还有 {{ job.queue_ahead }} 个任务</p>
    </Transition>

    <div v-if="isBatch && batch" class="batch">
      <div class="batch-head">
        <span class="eyebrow">批量进度</span>
        <span class="batch-stats mono">
          <span>{{ batch.succeeded + batch.failed }}/{{ batch.total }}</span>
          <span class="ok">{{ batch.succeeded }} 成功</span>
          <span v-if="batch.failed" class="fail">{{ batch.failed }} 失败</span>
        </span>
      </div>
      <div class="bar"><div class="bar-fill live" :style="{ width: batchPct + '%' }" /></div>
      <ul v-if="batch.failures?.length" class="failures">
        <li v-for="(f, i) in batch.failures" :key="i">
          <XCircle /><span class="lbl">{{ f.label }}</span><span class="err">{{ f.error }}</span>
        </li>
      </ul>
    </div>

    <Transition name="v-fade">
      <div v-if="attempts.length" class="attempts">
        <span class="attempts-title"><Users />账号尝试</span>
        <div class="chips">
          <span
            v-for="a in attempts"
            :key="a.account_id"
            class="pill"
            :class="{ 'pill-ok': a.status === 'success', 'pill-danger': a.status === 'failed', 'pill-live': a.status === 'running' }"
          >
            <Loader2 v-if="a.status === 'running'" class="spin" />
            <CheckCircle2 v-else-if="a.status === 'success'" />
            <XCircle v-else />
            {{ a.username || a.account_id.slice(-6) }}
          </span>
        </div>
      </div>
    </Transition>

    <Transition name="v-fade">
      <p v-if="error" class="error-block"><AlertTriangle />{{ error }}</p>
    </Transition>
  </div>
</template>

<style scoped>
.dock { display: flex; flex-direction: column; gap: 11px; }
.message { font-size: var(--fs-sm); color: var(--ink-2); }
.queue { font-size: var(--fs-xs); color: var(--ink-3); }

.batch { display: flex; flex-direction: column; gap: 7px; padding: 11px 12px; border-radius: var(--r-md); background: var(--panel-2); border: 1px solid var(--line); }
.batch-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.batch-stats { display: flex; gap: 10px; font-size: var(--fs-xs); color: var(--ink-2); }
.batch-stats .ok { color: var(--ok); }
.batch-stats .fail { color: var(--danger-ink); }
.failures { display: flex; flex-direction: column; gap: 4px; margin-top: 2px; }
.failures li { display: flex; align-items: center; gap: 6px; font-size: var(--fs-xs); color: var(--ink-2); }
.failures svg { width: 12px; height: 12px; color: var(--danger); flex: none; }
.failures .lbl { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.failures .err { color: var(--ink-3); }

.attempts { display: flex; flex-direction: column; gap: 6px; }
.attempts-title { display: flex; align-items: center; gap: 5px; font-size: var(--fs-xs); color: var(--ink-3); font-weight: var(--fw-med); }
.attempts-title svg { width: 12px; height: 12px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
</style>
