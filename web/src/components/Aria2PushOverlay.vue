<script setup lang="ts">
import { Loader2 } from 'lucide-vue-next'
import { aria2 } from '../composables/useAria2'
</script>

<template>
  <Teleport to="body">
    <Transition name="v-veil">
      <div v-if="aria2.overlay.active" class="overlay" role="status" aria-live="polite">
        <Transition name="v-pop" appear>
          <div v-if="aria2.overlay.active" class="push panel">
            <Loader2 class="spin ico" />
            <div class="copy">
              <h2>正在推送到 aria2</h2>
              <p class="progress mono">{{ aria2.overlay.done }} / {{ aria2.overlay.total }}</p>
              <p class="detail">{{ aria2.overlay.name || '正在推送链接…' }}</p>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.overlay { z-index: 8500; }
.push { display: flex; align-items: center; gap: 16px; padding: 20px 24px; border-radius: var(--r-xl); min-width: 300px; box-shadow: var(--shadow-pop); }
.ico { width: 28px; height: 28px; color: var(--brand); }
.copy h2 { font-size: var(--fs-md); font-weight: var(--fw-semi); }
.progress { font-size: var(--fs-sm); color: var(--ink-2); margin-top: 2px; }
.detail { font-size: var(--fs-xs); color: var(--ink-3); margin-top: 2px; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
