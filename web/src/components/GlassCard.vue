<script setup lang="ts">
withDefaults(defineProps<{
  hover?: boolean
  rise?: boolean
  pad?: boolean
  seam?: boolean
}>(), { hover: false, rise: true, pad: true, seam: false })
</script>

<template>
  <div
    class="panel card"
    :class="{ hover, 'anim-rise': rise, pad, 'panel-seam': seam }"
  >
    <slot />
  </div>
</template>

<style scoped>
.card {
  transition: transform var(--t) var(--ease), box-shadow var(--t) var(--ease),
    border-color var(--t) var(--ease);
}
.pad { padding: var(--sp-5); }
.hover:hover {
  transform: translateY(-2px);
  box-shadow: var(--shadow-md);
  border-color: var(--brand-line);
}
</style>
