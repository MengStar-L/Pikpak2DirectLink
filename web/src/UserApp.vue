<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  ArrowLeft, CalendarClock, CheckCheck, ClockAlert, Files, Gauge, History, Hourglass, Inbox, KeyRound,
  Link2, LogOut, Mail, Radar, RefreshCw, Send, Settings2, Ticket, TriangleAlert, UserPlus, Waypoints, X,
} from 'lucide-vue-next'
import AuroraBg from './components/AuroraBg.vue'
import GlassCard from './components/GlassCard.vue'
import PrimaryButton from './components/PrimaryButton.vue'
import ResolveForm from './components/ResolveForm.vue'
import JobStatus from './components/JobStatus.vue'
import FileTree from './components/FileTree.vue'
import ResultList from './components/ResultList.vue'
import ToastHost from './components/ToastHost.vue'
import Aria2ConfigModal from './components/Aria2ConfigModal.vue'
import Aria2PushOverlay from './components/Aria2PushOverlay.vue'
import Aria2PushChoiceModal from './components/Aria2PushChoiceModal.vue'
import { api, setUnauthorizedHandler } from './lib/api'
import { useJob } from './composables/useJob'
import { aria2 } from './composables/useAria2'
import { toast } from './composables/useToast'
import { formatBytes, formatDateTime, formatRelative } from './lib/format'
import type { JobResult, ResolveHistoryDetail, ResolveHistorySummary, UserAuthConfig, UserStatusResponse } from './lib/types'

const view = ref<'gate' | 'portal'>('gate')
const status = ref<UserStatusResponse | null>(null)
const authConfig = ref<UserAuthConfig | null>(null)

const emailMode = ref<'login' | 'register'>('login')
const email = ref('')
const password = ref('')
const loginError = ref('')
const loginLoading = ref(false)

const subscriptionOpen = ref(false)
const redeemOpen = ref(false)
const redeemCode = ref('')
const redeemError = ref('')
const redeemLoading = ref(false)

const pushChoiceOpen = ref(false)
const pushChoiceResults = ref<JobResult[]>([])
const historyOpen = ref(false)
const historyLoading = ref(false)
const historyError = ref('')
const historyItems = ref<ResolveHistorySummary[]>([])
const historyDetail = ref<ResolveHistoryDetail | null>(null)
const historyUnavailable = ref<ResolveHistorySummary | null>(null)
const selectedIds = ref<string[]>([])

const CURRENT_JOB_KEY = 'pikpak2directlink.user.current_job_id'
const RESTORE_POLL_MS = 1200
let restoreTimer: number | undefined
let restoreGeneration = 0

const { job, phase, error, submitting, submit, selectItems } = useJob({
  create: (b) => api.u.jobs.create(b),
  get: (id) => api.u.jobs.get(id),
  select: (id, b) => api.u.jobs.select(id, b),
})

const needSelection = computed(() => phase.value === 'selection_required' && job.value?.items?.length)
const results = computed(() => {
  const j = job.value
  if (!j) return []
  if (j.results?.length) return j.results
  if (j.result) return [j.result]
  return []
})
const queuePill = computed(() => status.value?.queue)
const subscriptions = computed(() => status.value?.subscriptions ?? [])
const activeSubscriptions = computed(() => subscriptions.value.filter((s) => !s.expired && s.remaining_bytes > 0))
const historyResults = computed(() => historyDetail.value?.results ?? [])
const displayName = computed(() => status.value?.user.display_name || status.value?.user.email || 'User')
const canProxy = computed(() => Boolean(status.value?.quota.allow_proxy_available))

setUnauthorizedHandler(() => {
  clearCurrentJob()
  view.value = 'gate'
  status.value = null
  closeSubscriptions()
  closeRedeem()
  closeHistory()
})

async function loadAuthConfig() {
  try {
    authConfig.value = await api.u.authConfig()
  } catch {
    authConfig.value = null
  }
}

async function loadStatus() {
  try {
    status.value = await api.u.status()
    authConfig.value = status.value.auth
    view.value = 'portal'
  } catch {
    status.value = null
    view.value = 'gate'
    await loadAuthConfig()
  }
}

function linuxDoLogin() {
  if (!authConfig.value?.linuxdo_available) return
  window.location.href = authConfig.value.linuxdo_start_url || '/api/u/auth/linuxdo/start'
}

async function submitEmailAuth() {
  loginError.value = ''
  if (!email.value.trim() || password.value.length < 6) {
    loginError.value = '请输入邮箱和至少 6 位密码'
    return
  }
  loginLoading.value = true
  try {
    status.value = emailMode.value === 'register'
      ? await api.u.emailRegister(email.value.trim(), password.value)
      : await api.u.emailLogin(email.value.trim(), password.value)
    authConfig.value = status.value.auth
    view.value = 'portal'
    await restoreCurrentJob()
    toast(emailMode.value === 'register' ? '账号已创建' : '已登录', 'success')
  } catch (e: any) {
    loginError.value = e?.message || '登录失败'
  } finally {
    loginLoading.value = false
  }
}

async function logout() {
  try {
    await api.u.logout()
  } catch { /* ignore */ }
  clearCurrentJob()
  closeSubscriptions()
  status.value = null
  view.value = 'gate'
  await loadAuthConfig()
}

function openSubscriptions() {
  subscriptionOpen.value = true
}
function closeSubscriptions() {
  subscriptionOpen.value = false
}
function openRedeem() {
  redeemCode.value = ''
  redeemError.value = ''
  redeemOpen.value = true
}
function closeRedeem() {
  redeemOpen.value = false
  redeemError.value = ''
  redeemLoading.value = false
}
async function submitRedeem() {
  redeemError.value = ''
  if (!redeemCode.value.trim()) {
    redeemError.value = '请输入 CDK'
    return
  }
  redeemLoading.value = true
  try {
    status.value = await api.u.redeemCDK(redeemCode.value.trim().toUpperCase())
    closeRedeem()
    toast('CDK 已兑换', 'success')
  } catch (e: any) {
    redeemError.value = e?.message || '兑换失败'
  } finally {
    redeemLoading.value = false
  }
}

async function onSubmit(payload: { input: string; passCode: string; mode: 'direct' | 'proxy' }) {
	invalidateRestore()
  removeStoredJobID()
  selectedIds.value = []
  await submit(payload.input, payload.passCode, payload.mode)
  if (job.value?.id) storeJobID(job.value.id)
}
function confirmSelection() {
  if (!selectedIds.value.length) {
    toast('请至少选择一个文件', 'error')
    return
  }
  selectItems(selectedIds.value)
}
function onPush(p: { url: string; name: string }) {
  aria2.pushOne(p.url, p.name)
}
function canChoosePushKind(list: JobResult[]) {
  return canProxy.value && list.length > 0 && list.every((r) => r.direct_url && r.proxy_url)
}
function pushManyAs(kind: 'direct' | 'proxy', list: JobResult[]) {
  aria2.pushMany(list.map((r) => ({ url: kind === 'proxy' ? r.proxy_url : r.direct_url, name: r.file.name })))
}
function pushAll() {
  const list = results.value
  if (canChoosePushKind(list)) {
    pushChoiceResults.value = list
    pushChoiceOpen.value = true
    return
  }
  pushManyAs('direct', list)
}
function closePushChoice() {
  pushChoiceOpen.value = false
  pushChoiceResults.value = []
}
function choosePushKind(kind: 'direct' | 'proxy') {
  const list = [...pushChoiceResults.value]
  closePushChoice()
  pushManyAs(kind, list)
}

function closeHistory() {
  historyOpen.value = false
  historyLoading.value = false
  historyError.value = ''
  historyItems.value = []
  historyDetail.value = null
  historyUnavailable.value = null
}
async function toggleHistory() {
  if (historyOpen.value) {
    closeHistory()
    return
  }
  historyOpen.value = true
  await loadHistory()
}
async function loadHistory() {
  historyLoading.value = true
  historyError.value = ''
  historyDetail.value = null
  historyUnavailable.value = null
  try {
    const payload = await api.u.history.list()
    historyItems.value = payload.history || []
  } catch (e: any) {
    historyError.value = e?.message || '加载历史失败'
  } finally {
    historyLoading.value = false
  }
}
async function openHistoryDetail(item: ResolveHistorySummary) {
  historyDetail.value = null
  historyUnavailable.value = null
  if (item.details_available === false) {
    historyUnavailable.value = item
    return
  }
  historyLoading.value = true
  historyError.value = ''
  try {
    historyDetail.value = await api.u.history.get(item.id)
  } catch (e: any) {
    if (e?.status === 410) {
      historyUnavailable.value = { ...item, details_available: false }
    } else if (e?.status === 404) {
      historyError.value = '这条历史记录已过期或不存在'
    } else {
      historyError.value = '暂时无法加载历史详情，请稍后重试'
    }
  } finally {
    historyLoading.value = false
  }
}
function backToHistoryList() {
  historyDetail.value = null
  historyUnavailable.value = null
}
function pushHistoryAll() {
  const list = historyResults.value
  if (canChoosePushKind(list)) {
    pushChoiceResults.value = list
    pushChoiceOpen.value = true
    return
  }
  pushManyAs('direct', list)
}
function historyInputPreview(input: string) {
  const lines = String(input || '').split(/\r?\n/).map((v) => v.trim()).filter(Boolean)
  if (!lines.length) return '-'
  if (lines.length > 1) return `${lines[0]} 等 ${lines.length} 条`
  return lines[0]
}
function historyKindLabel(kind: string) {
  if (kind === 'magnet') return '磁力'
  if (kind === 'share') return '分享'
  if (kind === 'batch') return '批量'
  return kind || '-'
}
function historyResultLabel(item: ResolveHistorySummary) {
  if (item.failure_code === 'service_restart') return '服务重启中断'
  if (item.status === 'failed' || item.failure_code) return '解析失败'
  if (item.batch?.total) return `成功 ${item.batch.succeeded}/${item.batch.total}`
  return `${item.result_count} 个结果`
}
function historyFailed(item: ResolveHistorySummary) {
  return item.status === 'failed' || Boolean(item.failure_code)
}
function historyDuration(item: ResolveHistorySummary | ResolveHistoryDetail) {
  const start = new Date(item.created_at).getTime()
  const end = new Date(item.completed_at).getTime()
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return '-'
  const sec = Math.max(1, Math.round((end - start) / 1000))
  if (sec < 60) return `${sec} 秒`
  const min = Math.floor(sec / 60)
  const rest = sec % 60
  if (min < 60) return rest ? `${min} 分 ${rest} 秒` : `${min} 分`
  const hr = Math.floor(min / 60)
  const minRest = min % 60
  return minRest ? `${hr} 小时 ${minRest} 分` : `${hr} 小时`
}

function clearRestoreTimer() {
  if (restoreTimer) {
    window.clearTimeout(restoreTimer)
    restoreTimer = undefined
  }
}

function invalidateRestore() {
	restoreGeneration += 1
	clearRestoreTimer()
}

function storedJobID() {
  try {
    return sessionStorage.getItem(CURRENT_JOB_KEY) || ''
  } catch {
    return ''
  }
}

function storeJobID(id: string) {
  try {
    sessionStorage.setItem(CURRENT_JOB_KEY, id)
  } catch { /* storage may be unavailable */ }
}

function removeStoredJobID() {
  try {
    sessionStorage.removeItem(CURRENT_JOB_KEY)
  } catch { /* storage may be unavailable */ }
}

function clearCurrentJob() {
	invalidateRestore()
  removeStoredJobID()
  job.value = null
  error.value = ''
}

function scheduleRestoredPoll(id: string, generation: number) {
	clearRestoreTimer()
	restoreTimer = window.setTimeout(() => pollRestoredJob(id, generation), RESTORE_POLL_MS)
}

async function pollRestoredJob(id: string, generation: number) {
	try {
		const restored = await api.u.jobs.get(id)
		if (generation !== restoreGeneration) return
		job.value = restored
    error.value = ''
    if (!['completed', 'failed', 'selection_required'].includes(restored.status)) {
			scheduleRestoredPoll(id, generation)
		}
	} catch (e: any) {
		if (generation !== restoreGeneration) return
    clearRestoreTimer()
    if (e?.status === 404 || e?.status === 410) {
      removeStoredJobID()
      job.value = null
      error.value = '上次任务的详细结果已过期，请重新解析'
      return
    }
    error.value = '暂时无法恢复上次任务，请稍后刷新页面'
  }
}

async function restoreCurrentJob() {
	const id = storedJobID()
	if (!id || !status.value) return
	const generation = ++restoreGeneration
	await pollRestoredJob(id, generation)
}

onMounted(async () => {
  const params = new URLSearchParams(window.location.search)
  const authError = params.get('error')
  if (authError) {
    toast(`LinuxDo 登录失败：${authError}`, 'error')
    window.history.replaceState({}, '', '/u')
  }
  await loadStatus()
  if (view.value === 'portal') await restoreCurrentJob()
})

onUnmounted(invalidateRestore)
</script>

<template>
  <AuroraBg subtle />
  <ToastHost />
  <Aria2ConfigModal />
  <Aria2PushOverlay />
  <Aria2PushChoiceModal
    :open="pushChoiceOpen"
    :count="pushChoiceResults.length"
    :show-proxy="canProxy"
    @select="choosePushKind"
    @close="closePushChoice"
  />

  <Teleport to="body">
    <Transition name="v-veil">
      <div v-if="subscriptionOpen" class="overlay subscription-overlay" @click.self="closeSubscriptions">
        <Transition name="v-pop" appear>
          <div v-if="subscriptionOpen" class="dialog subscription-dialog" role="dialog" aria-modal="true" aria-label="我的订阅">
            <div class="dialog-head subscription-dialog-head">
              <h2><Gauge />我的订阅</h2>
              <button type="button" class="dialog-close" aria-label="关闭" @click="closeSubscriptions"><X /></button>
            </div>
            <p class="subscription-note">解析会优先消耗最快到期的空间，不足时自动继续扣下一个订阅</p>
            <div class="subscription-dialog-body">
              <div v-if="subscriptions.length" class="subs">
                <div v-for="sub in subscriptions" :key="sub.id" class="sub-row" :class="{ expired: sub.expired }">
                  <span class="mono code">{{ sub.source_cdk_code || sub.id }}</span>
                  <span class="pill pill-ok">{{ sub.remaining_label }}</span>
                  <span class="pill">{{ sub.days_left }} 天</span>
                  <span class="pill" :class="sub.allow_proxy ? 'pill-brand' : ''">{{ sub.allow_proxy ? '中转' : '直链' }}</span>
                </div>
              </div>
              <div v-else class="empty subscription-empty"><Ticket /><p>暂无可用空间，请兑换 CDK</p></div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>

  <Teleport to="body">
    <Transition name="v-veil">
      <div v-if="redeemOpen" class="overlay redeem-overlay" @click.self="closeRedeem">
        <Transition name="v-pop" appear>
          <form v-if="redeemOpen" class="dialog redeem-dialog" role="dialog" aria-modal="true" aria-label="兑换 CDK" @submit.prevent="submitRedeem">
            <div class="dialog-head redeem-dialog-head">
              <h2><Ticket />兑换 CDK</h2>
              <button type="button" class="dialog-close" aria-label="关闭" @click="closeRedeem"><X /></button>
            </div>
            <label class="field">
              <span class="field-label">CDK</span>
              <input v-model="redeemCode" class="input input-mono redeem-input" type="text" autocomplete="off" placeholder="XXXX-XXXX-XXXX" />
            </label>
            <Transition name="v-fade"><p v-if="redeemError" class="error-block">{{ redeemError }}</p></Transition>
            <div class="dialog-actions">
              <button class="btn btn-ghost btn-sm" type="button" @click="closeRedeem">取消</button>
              <PrimaryButton type="submit" size="sm" :loading="redeemLoading"><template #icon><Ticket /></template>兑换</PrimaryButton>
            </div>
          </form>
        </Transition>
      </div>
    </Transition>
  </Teleport>

  <Teleport to="body">
    <Transition name="v-veil">
      <div v-if="historyOpen" class="overlay history-overlay" @click.self="closeHistory">
        <Transition name="v-pop" appear>
          <div v-if="historyOpen" class="dialog history-dialog" role="dialog" aria-modal="true" aria-label="解析历史">
            <div class="dialog-head history-dialog-head">
              <h2><History />解析历史</h2>
              <div class="history-dialog-actions">
                <PrimaryButton v-if="historyDetail || historyUnavailable" variant="line" size="sm" @click="backToHistoryList"><template #icon><ArrowLeft /></template>返回</PrimaryButton>
                <PrimaryButton variant="soft" size="sm" :loading="historyLoading" @click="loadHistory"><template #icon><RefreshCw /></template>刷新</PrimaryButton>
                <button type="button" class="dialog-close" aria-label="关闭" @click="closeHistory"><X /></button>
              </div>
            </div>
            <Transition name="v-fade"><p v-if="historyError" class="error-block">{{ historyError }}</p></Transition>
            <div class="history-dialog-body">
              <template v-if="historyDetail">
                <div class="history-detail-head">
                  <div>
                    <span class="eyebrow">{{ historyKindLabel(historyDetail.kind) }} · {{ historyResultLabel(historyDetail) }}</span>
                    <h3>{{ formatDateTime(historyDetail.completed_at) }}</h3>
                  </div>
                  <div class="history-metrics">
                    <span class="pill">用时 {{ historyDuration(historyDetail) }}</span>
                    <span v-if="historyDetail.charged_bytes > 0" class="pill pill-info">计费 {{ formatBytes(historyDetail.charged_bytes) }}</span>
                    <span class="pill pill-live">{{ historyDetail.details_available === false ? '详情已过期' : `${formatRelative(historyDetail.expires_at)}过期` }}</span>
                  </div>
                </div>
                <pre class="history-input mono">{{ historyDetail.input }}</pre>
                <div class="sec-head mb compact">
                  <div><span class="eyebrow">output · {{ historyResults.length }}</span><h2>历史结果</h2></div>
                  <div class="res-actions">
                    <button class="link-btn" type="button" @click="aria2.openConfig()"><Settings2 />aria2</button>
                    <PrimaryButton v-if="historyResults.length > 1" variant="soft" size="sm" @click="pushHistoryAll"><template #icon><Send /></template>全部推送</PrimaryButton>
                  </div>
                </div>
                <ResultList :results="historyResults" show-push @push="onPush" />
              </template>
              <template v-else-if="historyUnavailable">
                <div class="history-unavailable">
                  <ClockAlert />
                  <div>
                    <span class="eyebrow">details expired</span>
                    <h3>详细结果已过期</h3>
                    <p>任务摘要仍会保留，但下载地址和其它敏感详情已按保留策略清除。</p>
                    <div class="history-metrics">
                      <span class="tag">{{ historyKindLabel(historyUnavailable.kind) }}</span>
                      <span class="pill" :class="historyFailed(historyUnavailable) ? 'pill-danger' : 'pill-ok'">{{ historyResultLabel(historyUnavailable) }}</span>
                      <span v-if="historyUnavailable.charged_bytes > 0" class="pill pill-info">计费 {{ formatBytes(historyUnavailable.charged_bytes) }}</span>
                    </div>
                  </div>
                </div>
                <pre v-if="historyUnavailable.input" class="history-input mono">{{ historyUnavailable.input }}</pre>
              </template>
              <template v-else>
                <div v-if="historyLoading" class="history-state mono">加载中...</div>
                <div v-else-if="historyItems.length" class="history-list">
                  <button v-for="item in historyItems" :key="item.id" class="history-item" :class="{ expired: item.details_available === false }" type="button" @click="openHistoryDetail(item)">
                    <span class="history-main">
                      <span class="history-title">{{ historyInputPreview(item.input) }}</span>
                      <span class="history-sub mono">{{ formatDateTime(item.completed_at) }} · 用时 {{ historyDuration(item) }}</span>
                    </span>
                    <span class="history-tags">
                      <span class="tag">{{ historyKindLabel(item.kind) }}</span>
                      <span class="pill" :class="historyFailed(item) ? 'pill-danger' : 'pill-ok'">{{ historyResultLabel(item) }}</span>
                      <span v-if="item.charged_bytes > 0" class="pill pill-info">{{ formatBytes(item.charged_bytes) }}</span>
                      <span class="pill pill-live">{{ item.details_available === false ? '详情已过期' : `${formatRelative(item.expires_at)}过期` }}</span>
                    </span>
                  </button>
                </div>
                <div v-else class="empty history-empty"><Inbox /><p>暂无解析历史</p></div>
              </template>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>

  <Transition name="v-fade" mode="out-in">
    <main v-if="view === 'gate'" class="gate-wrap" key="gate">
      <section class="gate-card panel anim-rise">
        <div class="wire"><span class="wire-pulse" /></div>
        <div class="mark"><Radar /></div>
        <h1>用户登录</h1>
        <p class="lede">LinuxDo 社区用户可直接登录，额度通过 CDK 兑换</p>
        <PrimaryButton v-if="authConfig?.linuxdo_available" block size="lg" @click="linuxDoLogin">
          <template #icon><Radar /></template>使用 LinuxDo 登录
        </PrimaryButton>
        <p v-else class="auth-note">LinuxDo 登录暂未开放</p>

        <form v-if="authConfig?.email_login_enabled || authConfig?.email_registration_enabled" class="email-form" @submit.prevent="submitEmailAuth">
          <div v-if="authConfig?.email_registration_enabled" class="seg email-tabs">
            <label class="seg-item"><input v-model="emailMode" type="radio" value="login" /><span><Mail />登录</span></label>
            <label class="seg-item"><input v-model="emailMode" type="radio" value="register" /><span><UserPlus />注册</span></label>
          </div>
          <input v-model="email" class="input" type="email" autocomplete="email" placeholder="email@example.com" />
          <input v-model="password" class="input" type="password" autocomplete="current-password" placeholder="Password" />
          <PrimaryButton type="submit" block :loading="loginLoading">
            <template #icon><KeyRound /></template>{{ emailMode === 'register' ? '邮箱注册' : '邮箱登录' }}
          </PrimaryButton>
          <Transition name="v-fade"><p v-if="loginError" class="error-block">{{ loginError }}</p></Transition>
        </form>
      </section>
    </main>

    <main v-else class="portal" key="portal">
      <header class="phead panel">
        <div class="brand">
          <span class="logo"><Radar /></span>
          <div>
            <div class="title">PikPak 直链工具</div>
            <div class="sub mono">{{ displayName }}</div>
          </div>
        </div>
        <div class="pills">
          <span v-if="queuePill?.active" class="pill pill-live"><Hourglass />队列 {{ queuePill.waiting }}</span>
          <span class="pill pill-ok"><Gauge />剩余 {{ status?.quota.total_remaining_label || '0 B' }}</span>
          <span class="pill pill-info"><CalendarClock />{{ activeSubscriptions[0]?.days_left ?? '-' }} 天</span>
          <span class="pill" :class="canProxy ? 'pill-brand' : ''"><Waypoints />中转{{ canProxy ? '可用' : '不可用' }}</span>
          <button class="btn btn-ghost btn-sm" type="button" @click="aria2.openConfig()"><Settings2 />aria2</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="openSubscriptions"><Gauge />我的订阅</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="openRedeem"><Ticket />兑换</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="toggleHistory"><History />历史</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="logout"><LogOut />退出</button>
        </div>
      </header>

      <GlassCard class="workbench" seam>
        <div class="sec-head">
          <div class="sec-title">
            <span class="sec-glyph"><Link2 /></span>
            <div><span class="eyebrow">resolve</span><h2>链接解析</h2><p>粘贴磁力或 PikPak 分享链接，勾选文件生成下载链接</p></div>
          </div>
        </div>
        <ResolveForm :loading="submitting" :allow-proxy="canProxy" @submit="onSubmit" />
        <div class="dock-wrap"><JobStatus :job="job" :phase="phase" :error="error" :submitting="submitting" /></div>
        <p v-if="job?.failure_code === 'service_restart'" class="job-retention-state interrupted"><TriangleAlert /><span>此任务因服务重启而中断，未自动重新执行。请确认额度状态后重新提交。</span></p>
        <p v-else-if="job?.details_available === false" class="job-retention-state"><ClockAlert /><span>此任务的详细结果已超过保留时间，请重新解析。</span></p>
      </GlassCard>

      <Transition name="v-rise">
        <GlassCard v-if="needSelection" :key="'sel'">
          <div class="sec-head mb">
            <div class="sec-title">
              <span class="sec-glyph live"><Files /></span>
              <div><span class="eyebrow">select</span><h2>选择文件</h2></div>
            </div>
            <PrimaryButton size="sm" :disabled="!selectedIds.length" @click="confirmSelection">
              <template #icon><CheckCheck /></template>生成链接 {{ selectedIds.length }}
            </PrimaryButton>
          </div>
          <FileTree :items="job?.items ?? []" v-model="selectedIds" />
        </GlassCard>
      </Transition>

      <Transition name="v-rise">
        <GlassCard v-if="results.length" :key="'res'">
          <div class="sec-head mb">
            <div class="sec-title">
              <span class="sec-glyph ok"><CheckCheck /></span>
              <div><span class="eyebrow">output · {{ results.length }}</span><h2>下载链接</h2></div>
            </div>
            <div class="res-actions">
              <button class="link-btn" type="button" @click="aria2.openConfig()"><Settings2 />aria2</button>
              <PrimaryButton v-if="results.length > 1" variant="soft" size="sm" @click="pushAll"><template #icon><Send /></template>全部推送</PrimaryButton>
            </div>
          </div>
          <ResultList :results="results" show-push @push="onPush" />
        </GlassCard>
      </Transition>
    </main>
  </Transition>
</template>

<style scoped>
.gate-wrap { position: relative; z-index: 1; min-height: 100vh; display: grid; place-items: center; padding: 24px; }
.gate-card { position: relative; width: min(100%, 392px); padding: 30px 28px 24px; text-align: center; overflow: hidden; }
.wire { position: absolute; left: 0; right: 0; top: 0; height: 2px; background: var(--canvas-2); overflow: hidden; }
.wire-pulse { position: absolute; top: 0; left: 0; width: 36%; height: 100%; background: linear-gradient(90deg, transparent, var(--brand), transparent); animation: wireRun 2.6s var(--ease) infinite; }
.mark { width: 46px; height: 46px; margin: 4px auto 14px; display: grid; place-items: center; border-radius: var(--r-lg); background: var(--brand); color: var(--ink-on); box-shadow: var(--shadow-sm); }
.mark svg { width: 22px; height: 22px; }
.gate-card h1 { font-size: var(--fs-xl); font-weight: var(--fw-bold); }
.lede { color: var(--ink-2); font-size: var(--fs-sm); margin-top: 5px; margin-bottom: 18px; }
.auth-note { margin: 0 0 13px; color: var(--ink-3); font-size: var(--fs-sm); }
.email-form { display: grid; gap: 10px; margin-top: 14px; text-align: left; }
.email-tabs { width: 100%; }
.email-tabs .seg-item { flex: 1 1 0; }
.email-tabs .seg-item span { width: 100%; justify-content: center; }

.portal { position: relative; z-index: 1; width: 100%; max-width: 1760px; margin: 0 auto; padding: 20px 28px 56px; display: flex; flex-direction: column; gap: 14px; }
.phead { display: flex; align-items: center; justify-content: space-between; gap: 14px; flex-wrap: wrap; padding: 12px 16px; }
.brand { display: flex; align-items: center; gap: 11px; }
.logo { width: 36px; height: 36px; border-radius: var(--r-md); display: grid; place-items: center; background: var(--brand); color: var(--ink-on); flex: none; }
.logo svg { width: 19px; height: 19px; }
.title { font-size: var(--fs-md); font-weight: var(--fw-semi); }
.sub { color: var(--ink-3); font-size: var(--fs-xs); margin-top: 1px; }
.pills { display: flex; align-items: center; gap: 7px; flex-wrap: wrap; }
.workbench { display: flex; flex-direction: column; gap: 16px; }
.eyebrow { display: block; margin-bottom: 2px; }
.sec-glyph.ok { background: var(--ok-soft); color: var(--ok); }
.sec-glyph.live { background: var(--live-soft); color: var(--live); }
.sec-head.mb { margin-bottom: 14px; }
.sec-head.compact h2 { font-size: var(--fs-md); }
.dock-wrap { padding-top: 13px; border-top: 1px solid var(--line); }
.res-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.subs { display: grid; gap: 8px; }
.sub-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; padding: 9px 11px; border: 1px solid var(--line); border-radius: var(--r-md); background: var(--panel-2); }
.sub-row.expired { opacity: 0.55; }
.sub-row .code { flex: 1 1 180px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-2); font-size: var(--fs-xs); }
.subscription-dialog { width: min(680px, calc(100vw - 32px)); max-height: calc(100vh - 48px); display: flex; flex-direction: column; overflow: hidden; }
.subscription-dialog-head { flex: none; margin-bottom: 8px; }
.subscription-dialog-head h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.subscription-dialog-head h2 svg { width: 17px; height: 17px; color: var(--brand); }
.subscription-note { margin: 0 0 14px; color: var(--ink-2); font-size: var(--fs-sm); line-height: 1.55; }
.subscription-dialog-body { min-height: 0; overflow-y: auto; padding-right: 2px; }
.subscription-empty { min-height: 120px; }
.redeem-overlay { z-index: 7950; }
.redeem-dialog { width: min(430px, calc(100vw - 32px)); }
.redeem-dialog-head { margin-bottom: 14px; }
.redeem-dialog-head h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.redeem-dialog-head h2 svg { width: 17px; height: 17px; color: var(--brand); }
.redeem-input { text-transform: uppercase; }
.dialog-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; margin-top: 14px; }
.history-overlay { z-index: 7900; align-items: start; padding-top: 42px; }
.history-dialog { width: min(100%, 1180px); max-height: calc(100vh - 84px); display: flex; flex-direction: column; overflow: hidden; }
.history-dialog-head { flex: none; margin-bottom: 12px; }
.history-dialog-head h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.history-dialog-head h2 svg { width: 17px; height: 17px; color: var(--brand); }
.history-dialog-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
.history-dialog-body { min-height: 0; overflow-y: auto; padding-right: 2px; }
.history-list { display: grid; gap: 9px; }
.history-item { width: 100%; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 12px 13px; border: 1px solid var(--line); border-radius: var(--r-md); background: var(--panel-2); color: inherit; text-align: left; cursor: pointer; transition: transform var(--t) var(--ease), box-shadow var(--t) var(--ease), border-color var(--t) var(--ease); }
.history-item:hover { transform: translateY(-1px); box-shadow: var(--shadow-sm); border-color: var(--brand-soft); }
.history-item.expired { border-style: dashed; }
.history-main { min-width: 0; display: flex; flex-direction: column; gap: 3px; }
.history-title { font-size: var(--fs-sm); font-weight: var(--fw-semi); line-height: 1.45; word-break: break-all; }
.history-sub { font-size: var(--fs-2xs); color: var(--ink-3); }
.history-tags { flex: none; display: flex; align-items: center; justify-content: flex-end; gap: 6px; flex-wrap: wrap; }
.history-detail-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
.history-detail-head h3 { font-size: var(--fs-md); font-weight: var(--fw-semi); }
.history-metrics { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.history-input { margin: 0 0 14px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-md); background: var(--panel-2); color: var(--ink-2); font-size: var(--fs-xs); white-space: pre-wrap; word-break: break-all; }
.history-unavailable { display: flex; align-items: flex-start; gap: 12px; padding: 18px 0; }
.history-unavailable > svg { width: 24px; height: 24px; flex: none; color: var(--live-ink); }
.history-unavailable h3 { font-size: var(--fs-md); font-weight: var(--fw-semi); }
.history-unavailable p { max-width: 620px; margin: 4px 0 10px; color: var(--ink-2); font-size: var(--fs-sm); line-height: 1.55; }
.job-retention-state { display: flex; align-items: flex-start; gap: 8px; margin: -6px 0 0; padding: 10px 12px; border: 1px solid var(--live-line); border-radius: var(--r-md); color: var(--live-ink); background: var(--live-soft); font-size: var(--fs-sm); line-height: 1.5; }
.job-retention-state.interrupted { color: var(--danger-ink); background: var(--danger-soft); border-color: var(--danger-line); }
.job-retention-state svg { width: 16px; height: 16px; flex: none; margin-top: 2px; }
.history-state { padding: 16px 0; color: var(--ink-3); font-size: var(--fs-xs); }
.history-empty { min-height: 120px; }
@keyframes wireRun { 0% { left: -36%; } 100% { left: 100%; } }
@media (max-width: 600px) {
  .portal { padding: 16px 14px 48px; }
  .subscription-overlay { padding: 12px; }
  .subscription-dialog { width: 100%; max-height: calc(100vh - 24px); }
  .redeem-overlay { padding: 12px; }
  .redeem-dialog { width: 100%; }
  .dialog-actions { justify-content: stretch; }
  .dialog-actions .btn { flex: 1 1 0; }
  .history-overlay { align-items: stretch; padding: 12px; }
  .history-dialog { max-height: calc(100vh - 24px); }
  .history-dialog-head { align-items: flex-start; }
  .history-dialog-actions { width: 100%; justify-content: flex-start; }
  .history-item { align-items: flex-start; flex-direction: column; }
  .history-tags { justify-content: flex-start; }
}
</style>
