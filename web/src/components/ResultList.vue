<script setup lang="ts">
import ResultCard from './ResultCard.vue'
import type { JobResult } from '../lib/types'

defineProps<{
  results: JobResult[]
  showPush?: boolean
}>()

const emit = defineEmits<{
  (e: 'push', payload: { url: string; name: string }): void
}>()
</script>

<template>
  <div class="result-list">
    <TransitionGroup name="v-list">
      <ResultCard
        v-for="r in results"
        :key="r.file.id"
        :result="r"
        :show-push="showPush"
        @push="emit('push', $event)"
      />
    </TransitionGroup>
  </div>
</template>

<style scoped>
.result-list { position: relative; display: grid; grid-template-columns: 1fr; gap: 10px; }
@media (min-width: 1100px) {
  .result-list { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
