<script setup lang="ts">
// A faint instrument backdrop: a soft light at the top and two barely-there
// teal washes in the corners. Static and quiet — the dot-grid lives on <body>.
defineProps<{
  /** even quieter for dense pages */
  subtle?: boolean
}>()
</script>

<template>
  <div class="bg" :class="{ subtle }" aria-hidden="true">
    <span class="wash wash-top" />
    <span class="wash wash-teal" />
  </div>
</template>

<style scoped>
.bg {
  position: fixed;
  inset: 0;
  z-index: 0;
  pointer-events: none;
  overflow: hidden;
}
.wash { position: absolute; border-radius: 50%; filter: blur(60px); }
.wash-top {
  width: 90vw;
  height: 40vh;
  top: -22vh;
  left: 50%;
  transform: translateX(-50%);
  background: radial-gradient(closest-side, rgba(255, 255, 255, 0.9), transparent 75%);
  opacity: 0.8;
}
.wash-teal {
  width: 44vw;
  height: 44vw;
  min-width: 380px;
  min-height: 380px;
  bottom: -16vw;
  right: -12vw;
  background: radial-gradient(circle at 60% 60%, rgba(14, 140, 127, 0.14), transparent 70%);
  opacity: 0.7;
}
.subtle .wash-teal { opacity: 0.4; }
</style>
