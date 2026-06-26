<script setup lang="ts">
import { ref } from 'vue'
import { Copy, Check } from 'lucide-vue-next'
import { copyText } from '../lib/clipboard'

const props = defineProps<{
  text: string | (() => string)
  label?: string
  size?: 'sm' | 'md'
}>()

const copied = ref(false)
let timer: number | undefined

async function onClick() {
  const value = typeof props.text === 'function' ? props.text() : props.text
  const ok = await copyText(value)
  if (ok) {
    copied.value = true
    window.clearTimeout(timer)
    timer = window.setTimeout(() => (copied.value = false), 1500)
  }
}
</script>

<template>
  <button
    type="button"
    class="btn btn-line copy-btn"
    :class="[size === 'sm' && 'btn-sm', copied && 'is-copied']"
    @click="onClick"
  >
    <component :is="copied ? Check : Copy" :class="{ pop: copied }" />
    <span>{{ copied ? '已复制' : (label || '复制') }}</span>
  </button>
</template>

<style scoped>
.copy-btn.is-copied { color: var(--ok); border-color: var(--ok-line); background: var(--ok-soft); }
.copy-btn .pop { animation: pop-in var(--t) var(--spring); }
</style>
