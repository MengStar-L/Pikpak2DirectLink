<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  User, Crown, RefreshCw, RotateCcw, Trash2, Pencil, Save, X, AlertTriangle, CheckCircle2, XCircle,
} from 'lucide-vue-next'
import PrimaryButton from './PrimaryButton.vue'
import type { AccountSummary } from '../lib/types'
import { formatBytes, formatRelative, formatPercent } from '../lib/format'

const props = defineProps<{ account: AccountSummary; busy?: boolean }>()

const emit = defineEmits<{
  (e: 'update', id: string, trafficGb: number): void
  (e: 'delete', id: string): void
  (e: 'reset', id: string): void
  (e: 'refresh', id: string): void
}>()

const editing = ref(false)
const draftLimit = ref(Math.round(props.account.traffic_limit / (1 << 30)))

const trafficPct = computed(() => formatPercent(props.account.traffic_used, props.account.traffic_limit))
const failed = computed(() => props.account.status === 'failed')
const premiumUntil = computed(() => (props.account.premium_until ? formatRelative(props.account.premium_until) : '-'))
const nextCheck = computed(() => (props.account.credential_next_check_at ? formatRelative(props.account.credential_next_check_at) : '-'))

function startEdit() {
  draftLimit.value = Math.round(props.account.traffic_limit / (1 << 30)) || 700
  editing.value = true
}
function saveEdit() {
  const gb = Math.max(1, Math.floor(draftLimit.value || 1))
  emit('update', props.account.id, gb)
  editing.value = false
}
function cancelEdit() {
  editing.value = false
}
</script>

<template>
  <article class="acct panel anim-rise" :class="{ failed }">
    <header class="head">
      <span class="avatar"><User /></span>
      <div class="id">
        <h3 class="uname" :title="account.username">{{ account.username }}</h3>
        <div class="sub">
          <span class="pill" :class="failed ? 'pill-danger' : 'pill-ok'">
            <component :is="failed ? XCircle : CheckCircle2" />{{ failed ? '失败' : '可用' }}
          </span>
          <span v-if="account.premium" class="pill pill-live"><Crown />VIP · {{ premiumUntil }}</span>
          <span v-if="account.traffic_limited" class="pill pill-live">流量受限</span>
        </div>
      </div>
    </header>

    <div class="traffic">
      <div class="traffic-head">
        <span class="eyebrow">月度流量</span>
        <span class="val mono">{{ formatBytes(account.traffic_used) }} / {{ formatBytes(account.traffic_limit) }}</span>
      </div>
      <div class="bar"><div class="bar-fill" :class="{ warn: trafficPct > 85 }" :style="{ width: trafficPct + '%' }" /></div>
    </div>

    <div class="lines">
      <div v-if="account.last_error" class="errline">
        <AlertTriangle /><span>{{ account.last_error }}</span>
        <span v-if="account.last_failed_at" class="time">{{ formatRelative(account.last_failed_at) }}</span>
      </div>
      <div v-if="account.parse_error_count" class="errline warn">
        <AlertTriangle /><span>解析错误 {{ account.parse_error_count }} 次</span>
      </div>
      <div v-if="account.credential_check_error" class="errline">
        <AlertTriangle /><span>凭据检查：{{ account.credential_check_error }}</span>
      </div>
      <div class="meta">下次凭据检查：{{ nextCheck }}</div>
    </div>

    <footer class="actions">
      <div v-if="editing" class="edit-limit">
        <input v-model.number="draftLimit" class="input input-mono sm" type="number" min="1" step="1" inputmode="numeric" />
        <span class="unit">GB</span>
        <button class="btn btn-soft btn-sm" type="button" @click="saveEdit"><Save />保存</button>
        <button class="btn btn-ghost btn-sm btn-icon" type="button" @click="cancelEdit"><X /></button>
      </div>
      <template v-else>
        <button class="btn btn-line btn-sm" type="button" @click="startEdit"><Pencil />流量上限</button>
        <button v-if="failed" class="btn btn-line btn-sm" type="button" :disabled="busy" @click="emit('reset', account.id)"><RotateCcw />重置</button>
        <button class="btn btn-line btn-sm" type="button" :disabled="busy" @click="emit('refresh', account.id)"><RefreshCw />重登</button>
        <button class="btn btn-danger btn-sm btn-icon" type="button" :disabled="busy" @click="emit('delete', account.id)"><Trash2 /></button>
      </template>
    </footer>
  </article>
</template>

<style scoped>
.acct { padding: 15px 16px; display: flex; flex-direction: column; gap: 12px; transition: box-shadow var(--t) var(--ease), border-color var(--t) var(--ease); }
.acct:hover { box-shadow: var(--shadow-md); }
.acct.failed { border-color: var(--danger-line); }
.head { display: flex; align-items: center; gap: 11px; }
.avatar { width: 36px; height: 36px; border-radius: var(--r-md); display: grid; place-items: center; background: var(--brand-soft); color: var(--brand); flex: none; }
.avatar svg { width: 18px; height: 18px; }
.id { flex: 1 1 auto; min-width: 0; }
.uname { font-size: var(--fs-md); font-weight: var(--fw-semi); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sub { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 5px; }

.traffic { display: flex; flex-direction: column; gap: 6px; }
.traffic-head { display: flex; align-items: center; justify-content: space-between; }
.traffic-head .val { font-size: var(--fs-xs); color: var(--ink-2); }

.lines { display: flex; flex-direction: column; gap: 5px; }
.errline { display: flex; align-items: center; gap: 6px; font-size: var(--fs-xs); color: var(--danger-ink); line-height: 1.4; }
.errline.warn { color: var(--live-ink); }
.errline svg { width: 12px; height: 12px; flex: none; }
.errline span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.errline .time { color: var(--ink-3); margin-left: auto; flex: none; }
.meta { font-size: var(--fs-xs); color: var(--ink-3); }

.actions { display: flex; flex-wrap: wrap; gap: 6px; padding-top: 11px; border-top: 1px solid var(--line); }
.edit-limit { display: flex; align-items: center; gap: 6px; width: 100%; }
.input.sm { width: 84px; height: 28px; }
.unit { font-size: var(--fs-xs); color: var(--ink-3); margin-right: auto; }
</style>
