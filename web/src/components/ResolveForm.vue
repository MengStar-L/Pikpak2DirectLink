<script setup lang="ts">
import { ref, computed } from 'vue'
import { Link2, Play, Waypoints } from 'lucide-vue-next'
import PrimaryButton from './PrimaryButton.vue'

const props = defineProps<{
  loading?: boolean
  disabled?: boolean
  compact?: boolean
  allowProxy?: boolean
}>()

const emit = defineEmits<{
  (e: 'submit', payload: { input: string; passCode: string; mode: 'direct' | 'proxy' }): void
}>()

const input = ref('')
const passCode = ref('')
const mode = ref<'direct' | 'proxy'>('direct')

const lineCount = computed(() => input.value.split('\n').filter((l) => l.trim()).length)
const isBatch = computed(() => lineCount.value > 1)

function onSubmit() {
  if (props.loading || props.disabled) return
  if (!input.value.trim()) return
  emit('submit', { input: input.value, passCode: passCode.value, mode: props.allowProxy ? mode.value : 'direct' })
}
</script>

<template>
  <form class="rform" :class="{ compact }" @submit.prevent="onSubmit">
    <div class="main">
      <textarea
        v-model="input"
        class="textarea"
        :rows="compact ? 4 : 5"
        placeholder="magnet:?xt=urn:btih:...
https://mypikpak.com/s/...
每行一个链接 = 批量解析"
        aria-label="磁力链接或 PikPak 分享链接"
      />
    </div>

    <div class="side">
      <label class="field">
        <span class="field-label">提取码（可选）</span>
        <input v-model="passCode" class="input input-mono" type="text" autocomplete="off" placeholder="-" />
      </label>

      <div class="seg mode-seg">
        <label class="seg-item">
          <input v-model="mode" type="radio" value="direct" />
          <span><Link2 />Direct</span>
        </label>
        <label class="seg-item" :class="{ off: !allowProxy }" :title="allowProxy ? 'Proxy' : 'No proxy quota'">
          <input v-model="mode" type="radio" value="proxy" :disabled="!allowProxy" />
          <span><Waypoints />Proxy</span>
        </label>
      </div>

      <PrimaryButton class="submit-btn" type="submit" block size="lg" :loading="loading" :disabled="disabled || !input.trim()">
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
.submit-btn { margin-top: auto; }
.mode-seg { width: 100%; }
.mode-seg .seg-item { flex: 1 1 0; }
.mode-seg .seg-item span { width: 100%; justify-content: center; }
.mode-seg .off { opacity: 0.55; cursor: not-allowed; }

@media (max-width: 760px) {
  .rform { grid-template-columns: 1fr; }
  .submit-btn { margin-top: 0; }
}
</style>
