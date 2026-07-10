<script setup lang="ts">
import { ref } from 'vue'
import { CalendarDays, Pencil, Save, Ticket, Trash2, UserRoundSearch, X } from 'lucide-vue-next'
import CopyButton from './CopyButton.vue'
import { formatDateTime } from '../lib/format'
import type { CDKView } from '../lib/types'

const props = defineProps<{ cdk: CDKView; busy?: boolean }>()

const emit = defineEmits<{
  (e: 'update', code: string, trafficGb: number, days: number, allowProxy: boolean): void
  (e: 'delete', code: string): void
  (e: 'open-user', userID: string): void
}>()

const editing = ref(false)
const draftGb = ref(5)
const draftDays = ref(30)
const draftAllowProxy = ref(true)
const confirmRevoke = ref(false)

function startEdit() {
  draftGb.value = Math.max(1, Math.round(props.cdk.grant_bytes / (1 << 30)) || 2)
  draftDays.value = Math.max(1, props.cdk.duration_days || 30)
  draftAllowProxy.value = props.cdk.allow_proxy
  confirmRevoke.value = false
  editing.value = true
}
function saveEdit() {
  emit('update', props.cdk.code, Math.max(1, Math.floor(draftGb.value || 1)), Math.max(1, Math.floor(draftDays.value || 1)), draftAllowProxy.value)
  editing.value = false
}

function revoke() {
  if (!confirmRevoke.value) {
    confirmRevoke.value = true
    return
  }
  emit('delete', props.cdk.code)
  confirmRevoke.value = false
}
</script>

<template>
  <article class="cdk panel anim-rise" :class="{ inactive: cdk.status === 'revoked' }">
    <header class="head">
      <span class="tk"><Ticket /></span>
      <code class="code mono">{{ cdk.code }}</code>
      <CopyButton :text="cdk.code" label="复制" size="sm" />
      <span class="pill" :class="cdk.allow_proxy ? 'pill-brand' : ''" :title="cdk.allow_proxy ? '此 CDK 可用中转下载' : '此 CDK 不支持中转下载'">{{ cdk.allow_proxy ? '中转可用' : '无中转' }}</span>
      <span v-if="cdk.status === 'revoked'" class="pill pill-danger">已撤销</span>
      <span v-else-if="cdk.status === 'redeemed'" class="pill pill-ok">已核销</span>
      <span v-else class="pill pill-live">未兑换</span>
    </header>

    <div class="stats">
      <div class="stat"><span class="k">凭证面额</span><span class="v mono">{{ cdk.grant_label }}</span></div>
      <div class="stat"><span class="k">核销后有效期</span><span class="v mono">{{ cdk.duration_days }} 天</span></div>
      <div class="stat"><span class="k">创建时间</span><span class="v date"><CalendarDays />{{ formatDateTime(cdk.created_at) }}</span></div>
    </div>

    <div v-if="cdk.status !== 'unredeemed'" class="audit">
      <span v-if="cdk.status === 'redeemed'">核销于 {{ formatDateTime(cdk.redeemed_at || '') }}</span>
      <span v-else>撤销于 {{ formatDateTime(cdk.revoked_at || '') }}</span>
      <button v-if="cdk.status === 'redeemed' && cdk.redeemed_by_user_id" class="btn btn-line btn-sm" type="button" @click="emit('open-user', cdk.redeemed_by_user_id)"><UserRoundSearch />查看用户</button>
    </div>

    <footer v-if="cdk.status === 'unredeemed'" class="actions">
      <div v-if="editing" class="edit-row">
        <input v-model.number="draftGb" class="input input-mono sm" type="number" min="1" step="1" inputmode="numeric" /><span class="unit">GB</span>
        <input v-model.number="draftDays" class="input input-mono sm" type="number" min="1" step="1" inputmode="numeric" /><span class="unit">天</span>
        <label class="check edit-check"><input v-model="draftAllowProxy" type="checkbox" />中转</label>
        <button class="btn btn-soft btn-sm" type="button" @click="saveEdit"><Save />保存</button>
        <button class="btn btn-ghost btn-sm btn-icon" type="button" @click="editing = false"><X /></button>
      </div>
      <template v-else>
        <button class="btn btn-line btn-sm" type="button" :disabled="busy" @click="startEdit"><Pencil />修改凭证</button>
        <template v-if="confirmRevoke">
          <span class="confirm-copy">确认撤销？</span>
          <button class="btn btn-danger btn-sm" type="button" :disabled="busy" @click="revoke"><Trash2 />确认</button>
          <button class="btn btn-ghost btn-sm" type="button" :disabled="busy" @click="confirmRevoke = false">取消</button>
        </template>
        <button v-else class="btn btn-ghost btn-sm danger-link" type="button" :disabled="busy" @click="revoke"><Trash2 />撤销</button>
      </template>
    </footer>
  </article>
</template>

<style scoped>
.cdk { padding: 14px 15px; display: flex; flex-direction: column; gap: 11px; transition: box-shadow var(--t) var(--ease); }
.cdk:hover { box-shadow: var(--shadow-md); }
.cdk.inactive { background: var(--panel-2); }
.head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.tk { width: 28px; height: 28px; border-radius: var(--r-sm); display: grid; place-items: center; background: var(--brand-soft); color: var(--brand); flex: none; }
.tk svg { width: 15px; height: 15px; }
.code { flex: 1 1 auto; min-width: 0; font-size: var(--fs-sm); font-weight: var(--fw-semi); color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; letter-spacing: 0.04em; }
.stats { display: grid; grid-template-columns: repeat(3, 1fr); gap: 7px; }
.stat { display: flex; flex-direction: column; gap: 2px; padding: 8px 10px; border-radius: var(--r-sm); background: var(--panel-2); border: 1px solid var(--line); }
.stat .k { font-size: var(--fs-2xs); color: var(--ink-3); }
.stat .v { font-size: var(--fs-sm); font-weight: var(--fw-semi); color: var(--ink); }
.stat .date { display: flex; align-items: center; gap: 5px; font-family: var(--font-ui); font-size: var(--fs-xs); }
.stat .date svg { width: 13px; height: 13px; color: var(--ink-3); }
.audit { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 7px; padding-top: 10px; border-top: 1px solid var(--line); color: var(--ink-3); font-size: var(--fs-xs); }
.actions { display: flex; flex-wrap: wrap; gap: 6px; padding-top: 11px; border-top: 1px solid var(--line); }
.edit-row { display: flex; align-items: center; gap: 6px; width: 100%; flex-wrap: wrap; }
.input.sm { width: 74px; height: 28px; }
.unit { font-size: var(--fs-xs); color: var(--ink-3); }
.confirm-copy, .danger-link { color: var(--danger-ink); }
.confirm-copy { align-self: center; font-size: var(--fs-xs); font-weight: var(--fw-semi); }
@media (max-width: 440px) {
  .stats { grid-template-columns: 1fr; }
}
</style>
