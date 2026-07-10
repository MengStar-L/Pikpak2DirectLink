<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  AlertTriangle,
  CalendarClock,
  ChevronLeft,
  ChevronRight,
  Inbox,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  SquareUserRound,
  Ticket as TicketIcon,
  Trash2,
  UserRound,
  UsersRound,
  X,
} from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import Skeleton from '../Skeleton.vue'
import { api } from '../../lib/api'
import { formatDateTime } from '../../lib/format'
import { toast } from '../../composables/useToast'
import type {
  AdminSubscription,
  AdminUserDetail,
  AdminUserSummary,
  ApiError,
  UpdateAdminSubscriptionRequest,
} from '../../lib/types'

const props = defineProps<{ focusUserId?: string }>()
const emit = defineEmits<{ (e: 'focus-consumed'): void }>()

const GIB = 1024 ** 3
const PAGE_SIZE = 50

const users = ref<AdminUserSummary[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(true)
const searchInput = ref('')
const query = ref('')

const selectedUserID = ref('')
const detail = ref<AdminUserDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref('')

const creating = ref(false)
const createError = ref('')
const createForm = ref(newCreateForm())

type EditDraft = {
  id: string
  revision: number
  remainingGb: number
  expiresLocal: string
  allowProxy: boolean
  remainingChanged: boolean
  expiresChanged: boolean
  proxyChanged: boolean
}

const editDraft = ref<EditDraft | null>(null)
const editError = ref('')
const busySubscriptionID = ref('')
const confirmTerminateID = ref('')

const pageNumber = computed(() => Math.floor(offset.value / PAGE_SIZE) + 1)
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))
const canPrevious = computed(() => offset.value > 0)
const canNext = computed(() => offset.value + PAGE_SIZE < total.value)

function newCreateForm() {
  return { remainingGb: 2, expiresLocal: futureLocalDate(30), allowProxy: true }
}

function futureLocalDate(days: number) {
  return toDateTimeLocal(new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString())
}

function toDateTimeLocal(iso: string) {
  const date = new Date(iso)
  if (!Number.isFinite(date.getTime())) return ''
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000)
  return local.toISOString().slice(0, 16)
}

function localToISO(value: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toISOString() : ''
}

function gbToBytes(gb: number) {
  return Math.round(gb * GIB)
}

function bytesToGB(bytes: number) {
  return Number((bytes / GIB).toFixed(6))
}

function displayName(item: AdminUserSummary | AdminUserDetail) {
  return item.user.display_name || item.user.email || `用户 ${item.user.id.slice(0, 8)}`
}

function providerLabel(provider: string) {
  const labels: Record<string, string> = { email: '邮箱', linuxdo: 'Linux DO' }
  return labels[provider.toLowerCase()] || provider
}

function statusLabel(status: AdminSubscription['status']) {
  return { active: '有效', exhausted: '已用尽', expired: '已过期', terminated: '已终止' }[status]
}

function statusClass(status: AdminSubscription['status']) {
  if (status === 'active') return 'pill-ok'
  if (status === 'terminated') return 'pill-danger'
  return ''
}

async function load(showLoading = true) {
  if (showLoading) loading.value = true
  try {
    const response = await api.users.list(query.value, PAGE_SIZE, offset.value)
    users.value = response.users || []
    total.value = response.total
  } catch (e: any) {
    toast(e?.message || '加载注册用户失败', 'error')
  } finally {
    if (showLoading) loading.value = false
  }
}

function search() {
  query.value = searchInput.value.trim()
  offset.value = 0
  void load()
}

function changePage(direction: -1 | 1) {
  const next = offset.value + direction * PAGE_SIZE
  offset.value = Math.max(0, next)
  void load()
}

async function openUser(userID: string) {
  selectedUserID.value = userID
  detail.value = null
  editDraft.value = null
  confirmTerminateID.value = ''
  createError.value = ''
  createForm.value = newCreateForm()
  await loadDetail()
}

async function loadDetail() {
  const userID = selectedUserID.value
  if (!userID) return
  detailLoading.value = true
  detailError.value = ''
  try {
    const response = await api.users.get(userID)
    if (selectedUserID.value === userID) detail.value = response
  } catch (e: any) {
    if (selectedUserID.value === userID) detailError.value = e?.message || '加载用户详情失败'
  } finally {
    if (selectedUserID.value === userID) detailLoading.value = false
  }
}

function closeUser() {
  selectedUserID.value = ''
  detail.value = null
  editDraft.value = null
  confirmTerminateID.value = ''
}

async function refreshAfterChange() {
  await Promise.all([loadDetail(), load(false)])
}

async function createSubscription() {
  if (!detail.value) return
  createError.value = ''
  const expiresAt = localToISO(createForm.value.expiresLocal)
  if (!Number.isFinite(createForm.value.remainingGb) || createForm.value.remainingGb < 0.01) {
    createError.value = '额度至少为 0.01 GB'
    return
  }
  if (!expiresAt || new Date(expiresAt).getTime() <= Date.now()) {
    createError.value = '到期时间必须晚于当前时间'
    return
  }

  creating.value = true
  try {
    await api.users.createSubscription(detail.value.user.id, {
      remaining_bytes: gbToBytes(createForm.value.remainingGb),
      expires_at: expiresAt,
      allow_proxy: createForm.value.allowProxy,
    })
    createForm.value = newCreateForm()
    toast('订阅已添加', 'success')
    await refreshAfterChange()
  } catch (e: any) {
    createError.value = e?.message || '添加订阅失败'
  } finally {
    creating.value = false
  }
}

function startEdit(subscription: AdminSubscription) {
  confirmTerminateID.value = ''
  editError.value = ''
  editDraft.value = {
    id: subscription.id,
    revision: subscription.revision,
    remainingGb: bytesToGB(subscription.remaining_bytes),
    expiresLocal: toDateTimeLocal(subscription.expires_at),
    allowProxy: subscription.allow_proxy,
    remainingChanged: false,
    expiresChanged: false,
    proxyChanged: false,
  }
}

async function handleConflict() {
  editDraft.value = null
  confirmTerminateID.value = ''
  toast('订阅已在其他请求中发生变化，已刷新，请重新确认', 'info')
  await refreshAfterChange()
}

async function saveEdit() {
  if (!detail.value || !editDraft.value) return
  editError.value = ''
  const draft = editDraft.value
  const body: UpdateAdminSubscriptionRequest = { expected_revision: draft.revision }

  if (draft.remainingChanged) {
    if (!Number.isFinite(draft.remainingGb) || draft.remainingGb < 0) {
      editError.value = '剩余额度不能小于 0'
      return
    }
    body.remaining_bytes = gbToBytes(draft.remainingGb)
  }
  if (draft.expiresChanged) {
    const expiresAt = localToISO(draft.expiresLocal)
    if (!expiresAt || new Date(expiresAt).getTime() <= Date.now()) {
      editError.value = '到期时间必须晚于当前时间'
      return
    }
    body.expires_at = expiresAt
  }
  if (draft.proxyChanged) body.allow_proxy = draft.allowProxy
  if (!draft.remainingChanged && !draft.expiresChanged && !draft.proxyChanged) {
    editDraft.value = null
    return
  }

  busySubscriptionID.value = draft.id
  try {
    await api.users.updateSubscription(detail.value.user.id, draft.id, body)
    editDraft.value = null
    toast('订阅已更新', 'success')
    await refreshAfterChange()
  } catch (e: any) {
    if ((e as ApiError)?.status === 409) await handleConflict()
    else editError.value = e?.message || '更新订阅失败'
  } finally {
    busySubscriptionID.value = ''
  }
}

async function terminate(subscription: AdminSubscription) {
  if (!detail.value) return
  if (confirmTerminateID.value !== subscription.id) {
    editDraft.value = null
    confirmTerminateID.value = subscription.id
    return
  }

  busySubscriptionID.value = subscription.id
  try {
    await api.users.terminateSubscription(detail.value.user.id, subscription.id, {
      expected_revision: subscription.revision,
    })
    confirmTerminateID.value = ''
    toast('订阅已终止', 'success')
    await refreshAfterChange()
  } catch (e: any) {
    if ((e as ApiError)?.status === 409) await handleConflict()
    else toast(e?.message || '终止订阅失败', 'error')
  } finally {
    busySubscriptionID.value = ''
  }
}

function handleEscape(event: KeyboardEvent) {
  if (event.key === 'Escape' && selectedUserID.value) closeUser()
}

watch(
  () => props.focusUserId,
  (userID) => {
    if (!userID) return
    void openUser(userID)
    emit('focus-consumed')
  },
  { immediate: true },
)

onMounted(() => {
  void load()
  window.addEventListener('keydown', handleEscape)
})
onUnmounted(() => window.removeEventListener('keydown', handleEscape))
</script>

<template>
  <div class="page">
    <GlassCard seam>
      <div class="sec-head users-head">
        <div class="sec-title">
          <span class="sec-glyph"><UsersRound /></span>
          <div><span class="eyebrow">users</span><h2>注册用户</h2><p>用户身份与订阅权益</p></div>
        </div>
        <div class="head-actions">
          <form class="search-form" role="search" @submit.prevent="search">
            <label class="search-input">
              <Search />
              <span class="sr-only">搜索用户</span>
              <input v-model="searchInput" class="input" type="search" placeholder="名称或邮箱" autocomplete="off" />
            </label>
            <PrimaryButton type="submit" variant="line" size="sm">搜索</PrimaryButton>
          </form>
          <button class="btn btn-line btn-sm btn-icon" type="button" aria-label="刷新用户" title="刷新用户" :disabled="loading" @click="load()"><RefreshCw :class="{ spin: loading }" /></button>
        </div>
      </div>
    </GlassCard>

    <div class="users-panel panel">
      <template v-if="loading">
        <div class="skeleton-list">
          <Skeleton v-for="i in 5" :key="i" height="66px" radius="0" />
        </div>
      </template>
      <template v-else-if="users.length">
        <div class="user-grid user-grid-head" aria-hidden="true">
          <span>用户</span><span>认证</span><span>注册时间</span><span>剩余额度</span><span>订阅</span><span>到期 / 中转</span><span />
        </div>
        <div class="user-list">
          <article v-for="item in users" :key="item.user.id" class="user-grid user-row">
            <div class="identity">
              <span class="avatar">
                <UserRound />
                <img v-if="item.user.avatar_url" :src="item.user.avatar_url" alt="" referrerpolicy="no-referrer" />
              </span>
              <span class="identity-copy">
                <strong>{{ displayName(item) }}</strong>
                <span class="muted">{{ item.user.email || item.user.id }}</span>
              </span>
              <span v-if="item.user.disabled" class="pill pill-danger">已禁用</span>
            </div>
            <div class="data-cell"><span class="cell-label">认证</span><span class="provider-list"><span v-for="provider in item.auth_providers" :key="provider" class="pill">{{ providerLabel(provider) }}</span><span v-if="!item.auth_providers.length" class="muted">-</span></span></div>
            <div class="data-cell"><span class="cell-label">注册</span><time class="mono" :datetime="item.user.created_at">{{ formatDateTime(item.user.created_at) }}</time></div>
            <div class="data-cell"><span class="cell-label">剩余</span><strong class="mono">{{ item.quota.total_remaining_label }}</strong></div>
            <div class="data-cell"><span class="cell-label">订阅</span><span><strong class="mono">{{ item.active_subscription_count }}</strong><span class="muted"> / {{ item.subscription_count }}</span></span></div>
            <div class="data-cell expiry-cell"><span class="cell-label">到期 / 中转</span><span>{{ item.quota.next_expires_at ? formatDateTime(item.quota.next_expires_at) : '-' }}</span><span class="pill" :class="item.quota.allow_proxy_available ? 'pill-brand' : ''">{{ item.quota.allow_proxy_available ? '可中转' : '无中转' }}</span></div>
            <div class="row-action"><button class="btn btn-line btn-sm" type="button" @click="openUser(item.user.id)"><SquareUserRound />管理订阅</button></div>
          </article>
        </div>
      </template>
      <div v-else class="empty"><Inbox /><p>{{ query ? '没有匹配的注册用户' : '还没有用户注册' }}</p></div>

      <footer v-if="!loading && total > 0" class="pagination">
        <span class="muted">共 {{ total }} 位用户</span>
        <div class="page-controls">
          <button class="btn btn-line btn-sm btn-icon" type="button" aria-label="上一页" :disabled="!canPrevious" @click="changePage(-1)"><ChevronLeft /></button>
          <span class="mono">{{ pageNumber }} / {{ pageCount }}</span>
          <button class="btn btn-line btn-sm btn-icon" type="button" aria-label="下一页" :disabled="!canNext" @click="changePage(1)"><ChevronRight /></button>
        </div>
      </footer>
    </div>

    <Teleport to="body">
      <Transition name="v-fade">
        <div v-if="selectedUserID" class="overlay user-overlay" @click.self="closeUser">
          <section class="dialog user-dialog" role="dialog" aria-modal="true" aria-label="管理用户订阅">
            <header class="dialog-head">
              <div class="dialog-title">
                <h2><SquareUserRound />{{ detail ? displayName(detail) : '用户订阅' }}</h2>
                <p>{{ detail?.user.email || selectedUserID }}</p>
              </div>
              <button class="dialog-close" type="button" aria-label="关闭" @click="closeUser"><X /></button>
            </header>

            <div v-if="detailLoading && !detail" class="modal-loading"><RefreshCw class="spin" /><span>正在读取订阅</span></div>
            <div v-else-if="detailError && !detail" class="error-block"><AlertTriangle />{{ detailError }}<button class="btn btn-line btn-sm" type="button" @click="loadDetail">重试</button></div>
            <div v-else-if="detail" class="dialog-body">
              <section class="create-section">
                <div class="subsection-title"><div><h3>新增订阅</h3><p>直接向该用户发放独立订阅</p></div><span class="pill pill-brand">管理员发放</span></div>
                <form class="subscription-form create-form" @submit.prevent="createSubscription">
                  <label class="field"><span class="field-label">剩余额度 (GB)</span><input v-model.number="createForm.remainingGb" class="input input-mono" type="number" min="0.01" step="0.01" inputmode="decimal" /></label>
                  <label class="field"><span class="field-label">到期时间</span><input v-model="createForm.expiresLocal" class="input input-mono" type="datetime-local" /></label>
                  <label class="check proxy-check"><input v-model="createForm.allowProxy" type="checkbox" />允许中转下载</label>
                  <PrimaryButton type="submit" size="sm" :loading="creating"><template #icon><Plus /></template>新增订阅</PrimaryButton>
                </form>
                <Transition name="v-fade"><p v-if="createError" class="error-block">{{ createError }}</p></Transition>
              </section>

              <section class="subscriptions-section">
                <div class="subsection-title"><div><h3>订阅记录</h3><p>{{ detail.active_subscription_count }} 个有效，共 {{ detail.subscription_count }} 个</p></div><button class="btn btn-ghost btn-sm btn-icon" type="button" aria-label="刷新订阅" title="刷新订阅" :disabled="detailLoading" @click="loadDetail"><RefreshCw :class="{ spin: detailLoading }" /></button></div>

                <div v-if="detail.subscriptions.length" class="subscription-list">
                  <article v-for="subscription in detail.subscriptions" :key="subscription.id" class="subscription-row" :class="{ inactive: subscription.status !== 'active' }">
                    <header class="subscription-head">
                      <div class="subscription-source">
                        <span class="source-icon"><ShieldCheck v-if="subscription.source === 'admin'" /><TicketIcon v-else /></span>
                        <div><strong>{{ subscription.source === 'admin' ? '管理员发放' : 'CDK 核销' }}</strong><code v-if="subscription.source_cdk_code" class="mono">{{ subscription.source_cdk_code }}</code></div>
                      </div>
                      <div class="subscription-status"><span class="pill" :class="statusClass(subscription.status)">{{ statusLabel(subscription.status) }}</span><span class="mono muted">rev.{{ subscription.revision }}</span></div>
                    </header>

                    <div class="subscription-stats">
                      <div><span>剩余</span><strong class="mono">{{ subscription.remaining_label }}</strong></div>
                      <div><span>已用</span><strong class="mono">{{ subscription.used_label }}</strong></div>
                      <div><span>在途预留</span><strong class="mono">{{ subscription.reserved_label }}</strong></div>
                      <div><span>到期</span><strong>{{ formatDateTime(subscription.expires_at) }}</strong></div>
                    </div>

                    <div class="subscription-meta">
                      <span class="pill" :class="subscription.allow_proxy ? 'pill-brand' : ''">{{ subscription.allow_proxy ? '允许中转' : '仅直链' }}</span>
                      <span>创建于 {{ formatDateTime(subscription.created_at) }}</span>
                      <span v-if="subscription.terminated_at">终止于 {{ formatDateTime(subscription.terminated_at) }}</span>
                    </div>

                    <form v-if="editDraft?.id === subscription.id" class="subscription-form edit-form" @submit.prevent="saveEdit">
                      <label class="field"><span class="field-label">剩余额度 (GB)</span><input v-model.number="editDraft.remainingGb" class="input input-mono" type="number" min="0" step="0.01" inputmode="decimal" @input="editDraft.remainingChanged = true" /></label>
                      <label class="field"><span class="field-label">到期时间</span><input v-model="editDraft.expiresLocal" class="input input-mono" type="datetime-local" @input="editDraft.expiresChanged = true" /></label>
                      <label class="check proxy-check"><input v-model="editDraft.allowProxy" type="checkbox" @change="editDraft.proxyChanged = true" />允许中转下载</label>
                      <div class="edit-actions"><PrimaryButton type="submit" size="sm" :loading="busySubscriptionID === subscription.id">保存</PrimaryButton><button class="btn btn-ghost btn-sm" type="button" @click="editDraft = null">取消</button></div>
                      <p v-if="editError" class="error-block edit-error">{{ editError }}</p>
                    </form>

                    <footer v-else-if="subscription.status !== 'terminated'" class="subscription-actions">
                      <button class="btn btn-line btn-sm" type="button" :disabled="busySubscriptionID === subscription.id" @click="startEdit(subscription)"><Pencil />编辑</button>
                      <template v-if="confirmTerminateID === subscription.id">
                        <span class="confirm-copy">立即归零并终止？</span>
                        <button class="btn btn-danger btn-sm" type="button" :disabled="busySubscriptionID === subscription.id" @click="terminate(subscription)"><Trash2 />确认终止</button>
                        <button class="btn btn-ghost btn-sm" type="button" @click="confirmTerminateID = ''">取消</button>
                      </template>
                      <button v-else class="btn btn-ghost btn-sm danger-link" type="button" @click="terminate(subscription)"><Trash2 />终止</button>
                    </footer>
                  </article>
                </div>
                <div v-else class="empty subscription-empty"><CalendarClock /><p>该用户还没有订阅</p></div>
              </section>
            </div>
          </section>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 14px; }
.users-head { align-items: center; }
.eyebrow { display: block; margin-bottom: 2px; }
.head-actions, .search-form { display: flex; align-items: center; gap: 8px; }
.search-input { position: relative; display: block; width: min(260px, 32vw); }
.search-input svg { position: absolute; left: 10px; top: 9px; width: 15px; height: 15px; color: var(--ink-3); }
.search-input .input { padding-left: 32px; }

.users-panel { overflow: hidden; }
.user-grid { display: grid; grid-template-columns: minmax(220px, 1.5fr) minmax(90px, 0.65fr) 118px minmax(90px, 0.7fr) 72px minmax(155px, 1fr) auto; gap: 14px; align-items: center; }
.user-grid-head { padding: 9px 14px; background: var(--panel-2); border-bottom: 1px solid var(--line); color: var(--ink-3); font-size: var(--fs-2xs); font-weight: var(--fw-semi); text-transform: uppercase; }
.user-row { min-height: 68px; padding: 11px 14px; border-bottom: 1px solid var(--line); }
.user-row:last-child { border-bottom: 0; }
.user-row:hover { background: var(--panel-2); }
.identity { display: flex; align-items: center; gap: 9px; min-width: 0; }
.avatar { position: relative; display: grid; place-items: center; width: 36px; height: 36px; border-radius: 50%; overflow: hidden; flex: none; color: var(--brand); background: var(--brand-soft); }
.avatar > svg { width: 18px; height: 18px; }
.avatar img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; }
.identity-copy { display: flex; flex-direction: column; min-width: 0; }
.identity-copy strong, .identity-copy span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.identity-copy strong { font-size: var(--fs-sm); }
.identity-copy span { font-size: var(--fs-xs); }
.data-cell { min-width: 0; font-size: var(--fs-xs); color: var(--ink-2); }
.data-cell > strong { color: var(--ink); font-size: var(--fs-sm); }
.provider-list { display: flex; flex-wrap: wrap; gap: 4px; }
.expiry-cell { display: flex; align-items: center; flex-wrap: wrap; gap: 5px; }
.cell-label { display: none; }
.row-action { justify-self: end; }
.pagination { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 10px 14px; border-top: 1px solid var(--line); background: var(--panel-2); font-size: var(--fs-xs); }
.page-controls { display: flex; align-items: center; gap: 9px; }
.skeleton-list { display: flex; flex-direction: column; gap: 1px; background: var(--line); }

.user-overlay { z-index: 8100; }
.user-dialog { width: min(960px, calc(100vw - 32px)); max-height: min(88vh, 820px); padding: 0; display: flex; flex-direction: column; overflow: hidden; }
.dialog-head { flex: none; padding: 17px 20px 14px; margin: 0; border-bottom: 1px solid var(--line); }
.dialog-title { min-width: 0; }
.dialog-title h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.dialog-title h2 svg { width: 18px; height: 18px; color: var(--brand); }
.dialog-title p { margin-top: 2px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); font-size: var(--fs-xs); }
.dialog-body { min-height: 0; overflow-y: auto; }
.modal-loading { min-height: 240px; display: grid; place-content: center; justify-items: center; gap: 9px; color: var(--ink-3); }
.modal-loading svg { width: 22px; height: 22px; color: var(--brand); }
.user-dialog > .error-block { margin: 20px; align-items: center; }
.user-dialog > .error-block .btn { margin-left: auto; }
.create-section, .subscriptions-section { padding: 16px 20px; }
.create-section { border-bottom: 1px solid var(--line); background: var(--panel-2); }
.subsection-title { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.subsection-title h3 { font-size: var(--fs-md); }
.subsection-title p { margin-top: 1px; color: var(--ink-3); font-size: var(--fs-xs); }
.subscription-form { display: grid; grid-template-columns: minmax(150px, 0.7fr) minmax(220px, 1fr) auto auto; gap: 10px 14px; align-items: end; }
.proxy-check { min-height: 34px; white-space: nowrap; }
.create-section .error-block { margin-top: 10px; }
.subscription-list { border: 1px solid var(--line); border-radius: var(--r-lg); overflow: hidden; }
.subscription-row { padding: 14px; border-bottom: 1px solid var(--line); }
.subscription-row:last-child { border-bottom: 0; }
.subscription-row.inactive { background: var(--panel-2); }
.subscription-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.subscription-source { display: flex; align-items: center; gap: 9px; min-width: 0; }
.source-icon { display: grid; place-items: center; width: 28px; height: 28px; border-radius: var(--r-sm); color: var(--brand); background: var(--brand-soft); flex: none; }
.source-icon svg { width: 15px; height: 15px; }
.subscription-source > div { display: flex; flex-direction: column; min-width: 0; }
.subscription-source code { margin-top: 1px; color: var(--ink-3); font-size: var(--fs-2xs); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.subscription-status { display: flex; align-items: center; gap: 7px; font-size: var(--fs-2xs); }
.subscription-stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1px; margin-top: 12px; border: 1px solid var(--line); border-radius: var(--r-sm); overflow: hidden; background: var(--line); }
.subscription-stats > div { min-width: 0; padding: 8px 10px; display: flex; flex-direction: column; gap: 2px; background: var(--panel-2); }
.subscription-stats span { color: var(--ink-3); font-size: var(--fs-2xs); }
.subscription-stats strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-xs); }
.subscription-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 7px 12px; margin-top: 9px; color: var(--ink-3); font-size: var(--fs-xs); }
.subscription-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; margin-top: 11px; padding-top: 10px; border-top: 1px solid var(--line); }
.danger-link { color: var(--danger-ink); }
.confirm-copy { color: var(--danger-ink); font-size: var(--fs-xs); font-weight: var(--fw-semi); }
.edit-form { margin-top: 11px; padding-top: 11px; border-top: 1px solid var(--line); }
.edit-actions { display: flex; align-items: center; gap: 5px; }
.edit-error { grid-column: 1 / -1; }
.subscription-empty { padding: 28px 16px; border: 1px dashed var(--line-2); border-radius: var(--r-lg); }

@media (max-width: 1100px) {
  .user-grid { grid-template-columns: minmax(210px, 1.5fr) 90px 105px 72px minmax(140px, 1fr) auto; }
  .user-grid > :nth-child(3) { display: none; }
}
@media (max-width: 820px) {
  .users-head { align-items: flex-start; flex-direction: column; }
  .head-actions { width: 100%; }
  .search-form { flex: 1 1 auto; }
  .search-input { width: auto; flex: 1 1 auto; }
  .user-grid-head { display: none; }
  .user-row { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px 16px; }
  .user-row > :nth-child(3) { display: flex; }
  .identity { grid-column: 1 / -1; }
  .data-cell { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 24px; }
  .cell-label { display: inline; color: var(--ink-3); }
  .row-action { grid-column: 1 / -1; justify-self: stretch; }
  .row-action .btn { width: 100%; }
  .subscription-form { grid-template-columns: 1fr 1fr; }
  .edit-actions { justify-content: flex-end; }
}
@media (max-width: 560px) {
  .head-actions, .search-form { align-items: stretch; }
  .head-actions { flex-wrap: wrap; }
  .search-form { width: 100%; }
  .user-row { grid-template-columns: 1fr; padding: 13px; }
  .identity, .row-action { grid-column: 1; }
  .pagination { align-items: flex-start; }
  .user-overlay { padding: 8px; }
  .user-dialog { width: 100%; max-height: calc(100vh - 16px); border-radius: var(--r-lg); }
  .dialog-head, .create-section, .subscriptions-section { padding-left: 12px; padding-right: 12px; }
  .subscription-form { grid-template-columns: 1fr; }
  .subscription-form .btn { width: 100%; }
  .edit-actions { justify-content: stretch; }
  .edit-actions .btn { flex: 1 1 0; }
  .subscription-stats { grid-template-columns: 1fr 1fr; }
  .subscription-head { align-items: flex-start; }
  .subscription-status { align-items: flex-end; flex-direction: column; }
}
@media (max-width: 340px) {
  .search-form { flex-wrap: wrap; }
  .search-form .btn { width: 100%; }
  .subscription-stats { grid-template-columns: 1fr; }
}
</style>
