<script setup lang="ts">
import { ref } from 'vue'
import { Ticket, RefreshCw, Trash2, Save, X } from 'lucide-vue-next'
import CopyButton from './CopyButton.vue'
import type { CDKView } from '../lib/types'

const props = defineProps<{ cdk: CDKView; busy?: boolean }>()

const emit = defineEmits<{
  (e: 'update', code: string, trafficGb: number, days: number, allowProxy: boolean): void
  (e: 'delete', code: string): void
}>()

const editing = ref(false)
const draftGb = ref(5)
const draftDays = ref(30)
const draftAllowProxy = ref(true)

function startEdit() {
  draftGb.value = Math.max(1, Math.round(props.cdk.remaining_bytes / (1 << 30)) || 5)
  draftDays.value = Math.max(1, props.cdk.days_left || 30)
  draftAllowProxy.value = props.cdk.allow_proxy
  editing.value = true
}
function saveEdit() {
  emit('update', props.cdk.code, Math.max(1, Math.floor(draftGb.value || 1)), Math.max(1, Math.floor(draftDays.value || 1)), draftAllowProxy.value)
  editing.value = false
}
</script>

<template>
  <article class="cdk panel anim-rise" :class="{ expired: cdk.expired }">
    <header class="head">
      <span class="tk"><Ticket /></span>
      <code class="code mono">{{ cdk.code }}</code>
      <CopyButton :text="cdk.code" label="复制" size="sm" />
      <span class="pill" :class="cdk.allow_proxy ? 'pill-brand' : ''" :title="cdk.allow_proxy ? '此 CDK 可用中转下载' : '此 CDK 不支持中转下载'">{{ cdk.allow_proxy ? '中转可用' : '无中转' }}</span>
      <span v-if="cdk.expired" class="pill pill-danger">已过期</span>
    </header>

    <div class="stats">
      <div class="stat"><span class="k">剩余</span><span class="v mono">{{ cdk.remaining_label }}</span></div>
      <div class="stat"><span class="k">已用</span><span class="v mono">{{ cdk.used_label }}</span></div>
      <div class="stat"><span class="k">到期</span><span class="v mono">{{ cdk.days_left }} 天</span></div>
    </div>

    <footer class="actions">
      <div v-if="editing" class="edit-row">
        <input v-model.number="draftGb" class="input input-mono sm" type="number" min="1" step="1" inputmode="numeric" /><span class="unit">GB</span>
        <input v-model.number="draftDays" class="input input-mono sm" type="number" min="1" step="1" inputmode="numeric" /><span class="unit">天</span>
        <label class="check edit-check"><input v-model="draftAllowProxy" type="checkbox" />中转</label>
        <button class="btn btn-soft btn-sm" type="button" @click="saveEdit"><Save />保存</button>
        <button class="btn btn-ghost btn-sm btn-icon" type="button" @click="editing = false"><X /></button>
      </div>
      <template v-else>
        <button class="btn btn-line btn-sm" type="button" :disabled="busy" @click="startEdit"><RefreshCw />重置额度</button>
        <button class="btn btn-danger btn-sm btn-icon" type="button" :disabled="busy" @click="emit('delete', cdk.code)"><Trash2 /></button>
      </template>
    </footer>
  </article>
</template>

<style scoped>
.cdk { padding: 14px 15px; display: flex; flex-direction: column; gap: 11px; transition: box-shadow var(--t) var(--ease); }
.cdk:hover { box-shadow: var(--shadow-md); }
.cdk.expired { opacity: 0.6; }
.head { display: flex; align-items: center; gap: 8px; }
.tk { width: 28px; height: 28px; border-radius: var(--r-sm); display: grid; place-items: center; background: var(--brand-soft); color: var(--brand); flex: none; }
.tk svg { width: 15px; height: 15px; }
.code { flex: 1 1 auto; min-width: 0; font-size: var(--fs-sm); font-weight: var(--fw-semi); color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; letter-spacing: 0.04em; }
.stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 7px; }
.stat { display: flex; flex-direction: column; gap: 2px; padding: 8px 10px; border-radius: var(--r-sm); background: var(--panel-2); border: 1px solid var(--line); }
.stat .k { font-size: var(--fs-2xs); color: var(--ink-3); }
.stat .v { font-size: var(--fs-sm); font-weight: var(--fw-semi); color: var(--ink); }
.actions { display: flex; flex-wrap: wrap; gap: 6px; padding-top: 11px; border-top: 1px solid var(--line); }
.edit-row { display: flex; align-items: center; gap: 6px; width: 100%; flex-wrap: wrap; }
.input.sm { width: 74px; height: 28px; }
.unit { font-size: var(--fs-xs); color: var(--ink-3); }
</style>
