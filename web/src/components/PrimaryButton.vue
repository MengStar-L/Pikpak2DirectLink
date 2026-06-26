<script setup lang="ts">
withDefaults(defineProps<{
  type?: 'button' | 'submit' | 'reset'
  variant?: 'primary' | 'soft' | 'line' | 'ghost' | 'danger'
  size?: 'sm' | 'md' | 'lg'
  block?: boolean
  loading?: boolean
  disabled?: boolean
}>(), { type: 'button', variant: 'primary', size: 'md' })
</script>

<template>
  <button
    :type="type"
    :disabled="disabled || loading"
    class="btn"
    :class="[
      `btn-${variant}`,
      size === 'sm' && 'btn-sm',
      size === 'lg' && 'btn-lg',
      block && 'btn-block',
    ]"
  >
    <span v-if="loading" class="spinner" aria-hidden="true" />
    <slot v-else name="icon" />
    <slot />
  </button>
</template>
