<script setup lang="ts">
withDefaults(defineProps<{
  width?: string
  height?: string
  radius?: string
}>(), { height: '14px', radius: 'var(--r-sm)' })
</script>

<template>
  <span
    class="skeleton"
    :style="{ width: width ?? '100%', height, borderRadius: radius }"
    aria-hidden="true"
  />
</template>
