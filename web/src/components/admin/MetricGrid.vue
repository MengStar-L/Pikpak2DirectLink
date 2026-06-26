<script setup lang="ts">
import { toRef } from 'vue'
import { useAnimatedCounter } from '../../composables/useAnimatedCounter'

const props = defineProps<{
  total: number
  available: number
  failed: number
  running: number
  waiting: number
}>()

const cells = [
  { key: 'total', label: '账号总数', tone: 'brand', value: useAnimatedCounter(toRef(props, 'total')) },
  { key: 'avail', label: '可用', tone: 'ok', value: useAnimatedCounter(toRef(props, 'available')) },
  { key: 'fail', label: '失败', tone: 'danger', value: useAnimatedCounter(toRef(props, 'failed')) },
  { key: 'run', label: '解析中', tone: 'live', value: useAnimatedCounter(toRef(props, 'running')) },
  { key: 'wait', label: '排队', tone: 'info', value: useAnimatedCounter(toRef(props, 'waiting')) },
]
</script>

<template>
  <section class="strip panel" aria-label="运行概览">
    <div v-for="c in cells" :key="c.key" class="cell" :class="`t-${c.tone}`">
      <span class="dot" :class="{ live: c.key === 'run' && props.running > 0 }" />
      <div class="body">
        <span class="k">{{ c.label }}</span>
        <span class="v mono">{{ c.value.value }}</span>
      </div>
    </div>
  </section>
</template>

<style scoped>
.strip {
  display: flex;
  align-items: stretch;
  flex: none;
  padding: 0;
  overflow: hidden;
}
.cell {
  flex: 1 1 0;
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 11px 16px;
  min-width: 0;
}
.cell + .cell { border-left: 1px solid var(--line); }
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--ink-3); flex: none; }
.body { display: flex; flex-direction: column; line-height: 1.1; min-width: 0; }
.k { font-size: var(--fs-2xs); color: var(--ink-3); font-weight: var(--fw-med); }
.v { font-size: var(--fs-2xl); font-weight: var(--fw-semi); color: var(--ink); margin-top: 2px; }

.t-brand .dot { background: var(--brand); }
.t-ok .dot { background: var(--ok); }
.t-danger .dot { background: var(--danger); }
.t-live .dot { background: var(--live); }
.t-info .dot { background: var(--info); }
.t-danger .v { color: v-bind('props.failed > 0 ? "var(--danger-ink)" : "var(--ink)"'); }
.dot.live { animation: live-pulse 1.8s var(--ease) infinite; }

@media (max-width: 820px) {
  .strip { flex-wrap: wrap; }
  .cell { flex: 1 0 33%; }
  .cell:nth-child(3n + 1) { border-left: none; }
  .cell:nth-child(n + 4) { border-top: 1px solid var(--line); }
}
@media (max-width: 480px) {
  .cell { flex: 1 0 50%; }
  .cell:nth-child(3n + 1) { border-left: 1px solid var(--line); }
  .cell:nth-child(odd) { border-left: none; }
}
</style>
