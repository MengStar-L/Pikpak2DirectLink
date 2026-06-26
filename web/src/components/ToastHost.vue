<script setup lang="ts">
import { CheckCircle2, Info, XCircle, AlertTriangle } from 'lucide-vue-next'
import { useToasts, dismissToast, type ToastLevel } from '../composables/useToast'

const toasts = useToasts()

const icons: Record<ToastLevel, any> = {
  success: CheckCircle2,
  error: XCircle,
  info: Info,
}

// fallback icon for any unmapped level
import { Info as InfoFallback } from 'lucide-vue-next'
function iconFor(level: ToastLevel) {
  return icons[level] || InfoFallback
}
</script>

<template>
  <Teleport to="body">
    <div class="toast-host" role="status" aria-live="polite">
      <TransitionGroup name="toast">
        <div
          v-for="t in toasts"
          :key="t.id"
          class="toast"
          :class="`toast-${t.level}`"
          @click="dismissToast(t.id)"
        >
          <component :is="iconFor(t.level)" class="icon" />
          <span class="grow">{{ t.message }}</span>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>
