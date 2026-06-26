<script setup lang="ts">
import { Link2, Send, Waypoints, X } from 'lucide-vue-next'
import PrimaryButton from './PrimaryButton.vue'

defineProps<{
  open: boolean
  count: number
  showProxy: boolean
}>()

const emit = defineEmits<{
  (e: 'select', kind: 'direct' | 'proxy'): void
  (e: 'close'): void
}>()
</script>

<template>
  <Teleport to="body">
    <Transition name="v-veil">
      <div v-if="open" class="overlay" @click.self="emit('close')">
        <Transition name="v-pop" appear>
          <div v-if="open" class="dialog choice-dialog" role="dialog" aria-modal="true" aria-label="选择推送链接类型">
            <div class="dialog-head">
              <h2><Send />选择推送链接类型</h2>
              <button type="button" class="dialog-close" aria-label="关闭" @click="emit('close')"><X /></button>
            </div>
            <p class="hint">本次会推送 {{ count }} 个链接到 aria2。</p>

            <div class="actions">
              <PrimaryButton variant="line" size="sm" @click="emit('select', 'direct')">
                <template #icon><Link2 /></template>推送直链
              </PrimaryButton>
              <PrimaryButton v-if="showProxy" size="sm" @click="emit('select', 'proxy')">
                <template #icon><Waypoints /></template>推送中转链接
              </PrimaryButton>
              <button type="button" class="btn btn-ghost btn-sm" @click="emit('close')">取消</button>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.choice-dialog { max-width: 430px; }
.dialog-head h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.dialog-head h2 svg { width: 17px; height: 17px; color: var(--brand); }
.hint { font-size: var(--fs-sm); color: var(--ink-2); line-height: 1.55; margin-bottom: 16px; }
.actions { display: flex; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
</style>
