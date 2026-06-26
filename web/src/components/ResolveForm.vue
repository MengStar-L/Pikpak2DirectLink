<script setup lang="ts">
import { ref, computed } from 'vue'
import { Bolt, Database, Play, ArrowRight } from 'lucide-vue-next'
import PrimaryButton from './PrimaryButton.vue'
import type { JobMode } from '../lib/types'

const props = defineProps<{
  loading?: boolean
  disabled?: boolean
  compact?: boolean
}>()

const emit = defineEmits<{
  (e: 'submit', payload: { input: string; passCode: string; mode: JobMode }): void
}>()

const input = ref('')
const passCode = ref('')
const mode = ref<JobMode>('direct')

const lineCount = computed(() => input.value.split('\n').filter((l) => l.trim()).length)
const isBatch = computed(() => lineCount.value > 1)

function onSubmit() {
  if (props.loading || props.disabled) return
  if (!input.value.trim()) return
  emit('submit', { input: input.value, passCode: passCode.value, mode: mode.value })
}
</script>

<template>
  <form class="rform" :class="{ compact }" @submit.prevent="onSubmit">
    <div class="main">
      <textarea
        v-model="input"
        class="textarea"
        :rows="compact ? 4 : 5"
        placeholder="magnet:?xt=urn:btih:…&#10;https://mypikpak.com/s/…&#10;每行一个链接 = 批量解析"
        aria-label="磁力链接或 PikPak 分享链接"
      />
    </div>

    <div class="side">
      <label class="field">
        <span class="field-label">提取码（可选）</span>
        <input v-model="passCode" class="input input-mono" type="text" autocomplete="off" placeholder="——" />
      </label>

      <div class="field">
        <span class="field-label">链接方式</span>
        <div class="seg" role="radiogroup" aria-label="链接方式">
          <label class="seg-item">
            <input v-model="mode" type="radio" value="direct" />
            <span><Bolt />直链优先</span>
          </label>
          <label class="seg-item">
            <input v-model="mode" type="radio" value="proxy" />
            <span><Database />代理优先</span>
          </label>
        </div>
      </div>

      <PrimaryButton type="submit" block size="lg" :loading="loading" :disabled="disabled || !input.trim()">
        <template #icon><Play /></template>
        {{ isBatch ? `批量解析 ${lineCount} 条` : '开始解析' }}
      </PrimaryButton>
    </div>
  </form>
</template>

<style scoped>
.rform { display: grid; grid-template-columns: minmax(0, 1fr) 264px; gap: 14px; align-items: stretch; }
.main { display: flex; }
.main .textarea { width: 100%; min-height: 132px; }
.side { display: flex; flex-direction: column; gap: 12px; }
.side .field:nth-child(2) { margin-top: auto; }
.seg { width: 100%; }
.seg-item { flex: 1 1 0; }
.seg-item span { width: 100%; justify-content: center; }

@media (max-width: 760px) {
  .rform { grid-template-columns: 1fr; }
  .side .field:nth-child(2) { margin-top: 0; }
}
</style>
