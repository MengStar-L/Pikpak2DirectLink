// Global toast store. Each app instance is a single Vue root, so module-level
// reactive state is shared naturally within a page. Aria2 push notifications
// reuse the same toast() entry point.
import { reactive } from 'vue'

export type ToastLevel = 'success' | 'error' | 'info'
export type ToastItem = { id: number; message: string; level: ToastLevel }

const toasts = reactive<ToastItem[]>([])
let seq = 0

export function toast(message: string, level: ToastLevel = 'info'): void {
  const id = ++seq
  toasts.push({ id, message, level })
  window.setTimeout(() => {
    const i = toasts.findIndex((t) => t.id === id)
    if (i >= 0) toasts.splice(i, 1)
  }, 3000)
}

export function dismissToast(id: number): void {
  const i = toasts.findIndex((t) => t.id === id)
  if (i >= 0) toasts.splice(i, 1)
}

export function useToasts(): ToastItem[] {
  return toasts
}
