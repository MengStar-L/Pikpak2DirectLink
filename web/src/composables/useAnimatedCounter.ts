// Smooth count-up for metric cards. Animates from the previous value to the
// new one with an ease-out cubic over ~600ms; respects reduced-motion.
import { ref, watch, type Ref } from 'vue'

export function useAnimatedCounter(source: Ref<number>, duration = 600) {
  const display = ref(source.value)

  let raf = 0
  watch(source, (to, from) => {
    cancelAnimationFrame(raf)
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reduce || to === from) {
      display.value = to
      return
    }
    const start = performance.now()
    const a = from ?? 0
    const b = to
    const tick = (now: number) => {
      const t = Math.min(1, (now - start) / duration)
      const eased = 1 - Math.pow(1 - t, 3)
      display.value = Math.round(a + (b - a) * eased)
      if (t < 1) raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
  })

  return display
}
