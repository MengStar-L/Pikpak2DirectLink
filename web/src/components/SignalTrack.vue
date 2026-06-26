<script setup lang="ts">
// The signature element: a hairline "signal wire" that visualises a resolve
// job as it flows from a raw link to a direct link. Stage ticks + a fill that
// advances + a luminous pulse travelling the wire while the job is live.
import { computed } from 'vue'

const props = defineProps<{
  /** phase from useJob: idle | submitting | queued | running | selection_required | completed | failed */
  phase: string
  /** optional job.stage for precise placement on failure */
  stage?: string
}>()

const STAGES = [
  { k: 'queued', label: '排队' },
  { k: 'transfer', label: '转存' },
  { k: 'select', label: '选择' },
  { k: 'direct', label: '直链' },
] as const

type Mode = 'idle' | 'running' | 'paused' | 'done' | 'failed'

const state = computed<{ idx: number; mode: Mode }>(() => {
  switch (props.phase) {
    case 'submitting':
    case 'queued':
      return { idx: 0, mode: 'running' }
    case 'running':
      return { idx: 1, mode: 'running' }
    case 'selection_required':
      return { idx: 2, mode: 'paused' }
    case 'completed':
      return { idx: 3, mode: 'done' }
    case 'failed': {
      const map: Record<string, number> = {
        transfer: 1,
        source_selection: 2,
        result_selection: 2,
        complete: 3,
      }
      return { idx: props.stage && map[props.stage] != null ? map[props.stage] : 1, mode: 'failed' }
    }
    default:
      return { idx: 0, mode: 'idle' }
  }
})

const fill = computed(() => (state.value.idx / (STAGES.length - 1)) * 100)
const nodeLeft = (i: number) => `${(i / (STAGES.length - 1)) * 100}%`
</script>

<template>
  <div class="sig" :data-mode="state.mode">
    <div class="sig-wire">
      <span class="sig-fill" :style="{ width: fill + '%' }" />
      <span v-if="state.mode === 'running'" class="sig-pulse" aria-hidden="true" />
      <span
        v-for="(s, i) in STAGES"
        :key="s.k"
        class="sig-node"
        :class="{
          done: i < state.idx || state.mode === 'done',
          here: i === state.idx,
        }"
        :style="{ left: nodeLeft(i) }"
      />
    </div>
    <div class="sig-labels">
      <span
        v-for="(s, i) in STAGES"
        :key="s.k"
        class="sig-label"
        :class="{ on: i <= state.idx || state.mode === 'done', here: i === state.idx }"
      >{{ s.label }}</span>
    </div>
  </div>
</template>

<style scoped>
.sig { display: flex; flex-direction: column; gap: 9px; padding: 4px 6px 2px; }

.sig-wire {
  position: relative;
  height: 2px;
  margin: 6px 4px 0;
  background: var(--canvas-2);
  border-radius: var(--r-pill);
}

.sig-fill {
  position: absolute;
  left: 0;
  top: 0;
  height: 100%;
  border-radius: var(--r-pill);
  background: var(--brand);
  transition: width var(--t-slow) var(--ease-out), background var(--t) var(--ease);
}
[data-mode='paused'] .sig-fill {
  background-image: linear-gradient(90deg, var(--brand) 0%, #2fb3a4 50%, var(--brand) 100%);
  background-size: 220% 100%;
  animation: sigSheen 1.4s linear infinite;
}
[data-mode='failed'] .sig-fill { background: var(--danger); }

/* the travelling signal pulse */
.sig-pulse {
  position: absolute;
  top: 50%;
  width: 7px;
  height: 7px;
  margin: -3.5px 0 0 -3.5px;
  border-radius: 50%;
  background: var(--brand);
  box-shadow: 0 0 0 3px var(--brand-soft-2), 0 0 10px 1px rgba(14, 140, 127, 0.5);
  animation: sigRun 1.6s var(--ease) infinite;
}

.sig-node {
  position: absolute;
  top: 50%;
  width: 9px;
  height: 9px;
  margin: -4.5px 0 0 -4.5px;
  border-radius: 50%;
  background: var(--panel);
  border: 1.5px solid var(--line-2);
  transition: border-color var(--t) var(--ease), background var(--t) var(--ease), transform var(--t) var(--spring);
}
.sig-node.done { background: var(--brand); border-color: var(--brand); }
.sig-node.here { transform: scale(1.25); }
[data-mode='paused'] .sig-node.here { background: var(--live); border-color: var(--live); animation: live-pulse 1.7s var(--ease) infinite; }
[data-mode='running'] .sig-node.here { background: var(--brand); border-color: var(--brand); }
[data-mode='failed'] .sig-node.here { background: var(--danger); border-color: var(--danger); }
[data-mode='done'] .sig-node { background: var(--brand); border-color: var(--brand); }

.sig-labels { display: flex; justify-content: space-between; padding: 0 2px; }
.sig-label {
  font-family: var(--font-mono);
  font-size: var(--fs-2xs);
  letter-spacing: 0.08em;
  color: var(--ink-3);
  transition: color var(--t) var(--ease);
}
.sig-label.on { color: var(--brand-ink); }
.sig-label.here { color: var(--ink); font-weight: var(--fw-med); }
[data-mode='paused'] .sig-label.here { color: var(--live-ink); }
[data-mode='failed'] .sig-label.here { color: var(--danger-ink); }

@keyframes sigRun {
  0% { left: 0%; opacity: 0; }
  12% { opacity: 1; }
  88% { opacity: 1; }
  100% { left: 100%; opacity: 0; }
}
@keyframes sigSheen {
  0% { background-position: 160% 0; }
  100% { background-position: -160% 0; }
}
</style>
