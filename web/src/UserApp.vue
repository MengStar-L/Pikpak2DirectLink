<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Ticket, LogOut, Gauge, CalendarClock, Hourglass, Link2, Files, CheckCheck, Settings2, Send, Radar, Waypoints,
  History, ArrowLeft, RefreshCw, Inbox, X, GitMerge,
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
import { formatDateTime, formatRelative } from './lib/format'
import type { JobResult, ResolveHistoryDetail, ResolveHistorySummary, UserStatusResponse } from './lib/types'

const view = ref<'gate' | 'portal'>('gate')
const status = ref<UserStatusResponse | null>(null)
const cdkInput = ref('')
const cdkError = ref('')
const cdkLoading = ref(false)
const pushChoiceOpen = ref(false)
const pushChoiceResults = ref<JobResult[]>([])
const historyOpen = ref(false)
const historyLoading = ref(false)
const historyError = ref('')
const historyItems = ref<ResolveHistorySummary[]>([])
const historyDetail = ref<ResolveHistoryDetail | null>(null)
const mergeOpen = ref(false)
const mergePrimary = ref('')
const mergeSecondary = ref('')
const mergeError = ref('')
const mergeLoading = ref(false)

const { job, phase, error, submitting, submit, selectItems } = useJob({
  create: (b) => api.u.jobs.create(b),
  get: (id) => api.u.jobs.get(id),
  select: (id, b) => api.u.jobs.select(id, b),
})

const selectedIds = ref<string[]>([])
const needSelection = computed(() => phase.value === 'selection_required' && job.value?.items?.length)
const results = computed(() => {
  const j = job.value
  if (!j) return []
  if (j.results?.length) return j.results
  if (j.result) return [j.result]
  return []
})
const queuePill = computed(() => status.value?.queue)
const historyResults = computed(() => historyDetail.value?.results ?? [])

setUnauthorizedHandler(() => {
  view.value = 'gate'
  status.value = null
  closeMerge()
  closeHistory()
  toast('会话已过期，请重新输入 CDK', 'info')
})

async function loadStatus() {
  try {
    status.value = await api.u.status()
    view.value = 'portal'
  } catch {
    view.value = 'gate'
  }
}

async function cdkLogin() {
  cdkError.value = ''
  if (!cdkInput.value.trim()) {
    cdkError.value = '请输入 CDK 兑换码'
    return
  }
  cdkLoading.value = true
  try {
    status.value = await api.u.login(cdkInput.value.trim().toUpperCase())
    view.value = 'portal'
    toast('已进入用户面板', 'success')
  } catch (e: any) {
    cdkError.value = e?.message || 'CDK 无效或已过期'
  } finally {
    cdkLoading.value = false
  }
}

async function logout() {
  try {
    await api.u.logout()
  } catch { /* ignore */ }
  view.value = 'gate'
  status.value = null
  cdkInput.value = ''
  closeMerge()
  closeHistory()
}

function openMerge() {
  mergePrimary.value = status.value?.code || ''
  mergeSecondary.value = ''
  mergeError.value = ''
  mergeOpen.value = true
}
function closeMerge() {
  mergeOpen.value = false
  mergeError.value = ''
  mergeLoading.value = false
}
async function submitMerge() {
  const primary = mergePrimary.value.trim().toUpperCase()
  const secondary = mergeSecondary.value.trim().toUpperCase()
  mergeError.value = ''
  if (!primary || !secondary) {
    mergeError.value = '请输入主 CDK 和副 CDK'
    return
  }
  if (primary === secondary) {
    mergeError.value = '主 CDK 和副 CDK 不能相同'
    return
  }
  mergeLoading.value = true
  try {
    status.value = await api.u.mergeCDK(primary, secondary)
    closeMerge()
    toast('CDK 已合并', 'success')
  } catch (e: any) {
    const message = e?.message || 'CDK 合并失败'
    mergeError.value = message
    toast(message, 'error')
  } finally {
    mergeLoading.value = false
  }
}

function onSubmit(payload: { input: string; passCode: string; mode: 'direct' | 'proxy' }) {
  selectedIds.value = []
  submit(payload.input, payload.passCode, payload.mode)
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
  return Boolean(status.value?.allow_proxy) && list.length > 0 && list.every((r) => r.direct_url && r.proxy_url)
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
  try {
    const payload = await api.u.history.list()
    historyItems.value = payload.history || []
  } catch (e: any) {
    historyError.value = e?.message || '加载解析历史失败'
  } finally {
    historyLoading.value = false
  }
}
async function openHistoryDetail(id: string) {
  historyLoading.value = true
  historyError.value = ''
  try {
    historyDetail.value = await api.u.history.get(id)
  } catch (e: any) {
    historyError.value = e?.message || '加载历史详情失败'
  } finally {
    historyLoading.value = false
  }
}
function backToHistoryList() {
  historyDetail.value = null
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
  if (item.batch?.total) return `成功 ${item.batch.succeeded}/${item.batch.total}`
  return `${item.result_count} 个结果`
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

onMounted(loadStatus)
</script>

<template>
  <AuroraBg subtle />
  <ToastHost />
  <Aria2ConfigModal />
  <Aria2PushOverlay />
  <Aria2PushChoiceModal
    :open="pushChoiceOpen"
    :count="pushChoiceResults.length"
    :show-proxy="Boolean(status?.allow_proxy)"
    @select="choosePushKind"
    @close="closePushChoice"
  />

  <Teleport to="body">
    <Transition name="v-veil">
      <div v-if="mergeOpen" class="overlay merge-overlay" @click.self="closeMerge">
        <Transition name="v-pop" appear>
          <form v-if="mergeOpen" class="dialog merge-dialog" role="dialog" aria-modal="true" aria-label="合并 CDK" @submit.prevent="submitMerge">
            <div class="dialog-head merge-dialog-head">
              <h2><GitMerge />合并 CDK</h2>
              <button type="button" class="dialog-close" aria-label="关闭" @click="closeMerge"><X /></button>
            </div>
            <div class="merge-form">
              <label class="field">
                <span class="field-label">主 CDK</span>
                <input v-model="mergePrimary" class="input input-mono" type="text" autocomplete="off" placeholder="保留的 CDK" />
              </label>
              <label class="field">
                <span class="field-label">副 CDK</span>
                <input v-model="mergeSecondary" class="input input-mono" type="text" autocomplete="off" placeholder="合并后删除的 CDK" />
              </label>
            </div>
            <Transition name="v-fade">
              <p v-if="mergeError" class="error-block">{{ mergeError }}</p>
            </Transition>
            <div class="merge-actions">
              <button class="btn btn-ghost btn-sm" type="button" @click="closeMerge">取消</button>
              <PrimaryButton type="submit" size="sm" :loading="mergeLoading">
                <template #icon><GitMerge /></template>合并
              </PrimaryButton>
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
                <PrimaryButton v-if="historyDetail" variant="line" size="sm" @click="backToHistoryList"><template #icon><ArrowLeft /></template>返回列表</PrimaryButton>
                <PrimaryButton variant="soft" size="sm" :loading="historyLoading" @click="loadHistory"><template #icon><RefreshCw /></template>刷新</PrimaryButton>
                <button type="button" class="dialog-close" aria-label="关闭" @click="closeHistory"><X /></button>
              </div>
            </div>

            <Transition name="v-fade">
              <p v-if="historyError" class="error-block">{{ historyError }}</p>
            </Transition>

            <div class="history-dialog-body">
              <template v-if="historyDetail">
                <div class="history-detail-head">
                  <div>
                    <span class="eyebrow">{{ historyKindLabel(historyDetail.kind) }} · {{ historyResultLabel(historyDetail) }}</span>
                    <h3>{{ formatDateTime(historyDetail.completed_at) }}</h3>
                  </div>
                  <div class="history-metrics">
                    <span class="pill">用时 {{ historyDuration(historyDetail) }}</span>
                    <span class="pill pill-live">{{ formatRelative(historyDetail.expires_at) }}过期</span>
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

              <template v-else>
                <div v-if="historyLoading" class="history-state mono">加载中...</div>
                <div v-else-if="historyItems.length" class="history-list">
                  <button
                    v-for="item in historyItems"
                    :key="item.id"
                    class="history-item"
                    type="button"
                    @click="openHistoryDetail(item.id)"
                  >
                    <span class="history-main">
                      <span class="history-title">{{ historyInputPreview(item.input) }}</span>
                      <span class="history-sub mono">{{ formatDateTime(item.completed_at) }} · 用时 {{ historyDuration(item) }}</span>
                    </span>
                    <span class="history-tags">
                      <span class="tag">{{ historyKindLabel(item.kind) }}</span>
                      <span class="pill pill-ok">{{ historyResultLabel(item) }}</span>
                      <span class="pill pill-live">{{ formatRelative(item.expires_at) }}过期</span>
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
    <!-- CDK gate -->
    <main v-if="view === 'gate'" class="gate-wrap" key="gate">
      <section class="gate-card panel anim-rise">
        <div class="wire"><span class="wire-pulse" /></div>
        <div class="mark"><Ticket /></div>
        <h1>输入 CDK 进入</h1>
        <p class="lede">使用拿到的兑换码即可使用解析功能</p>
        <form class="cdk-form" @submit.prevent="cdkLogin">
          <input
            v-model="cdkInput"
            class="input input-mono cdk-input"
            type="text"
            autocomplete="off"
            placeholder="XXXX-XXXX-XXXX"
            aria-label="CDK 兑换码"
          />
          <PrimaryButton type="submit" block size="lg" :loading="cdkLoading">
            <template #icon><Ticket /></template>进入
          </PrimaryButton>
          <Transition name="v-fade">
            <p v-if="cdkError" class="error-block">{{ cdkError }}</p>
          </Transition>
        </form>
      </section>
    </main>

    <!-- Portal -->
    <main v-else class="portal" key="portal">
      <header class="phead panel">
        <div class="brand">
          <span class="logo"><Radar /></span>
          <div>
            <div class="title">PikPak 直链工具</div>
            <div class="sub mono">{{ status?.code || 'CDK 用户' }}</div>
          </div>
        </div>
        <div class="pills">
          <span v-if="queuePill?.active" class="pill pill-live"><Hourglass />队列 {{ queuePill.waiting }}</span>
          <span class="pill pill-ok"><Gauge />剩余 {{ status?.remaining_label || '-' }}</span>
          <span class="pill pill-info"><CalendarClock />{{ status?.days_left ?? '-' }} 天</span>
          <span class="pill" :class="status?.allow_proxy ? 'pill-brand' : ''" :title="status?.allow_proxy ? '此 CDK 支持中转下载' : '此 CDK 不支持中转下载'"><Waypoints />中转{{ status?.allow_proxy ? '可用' : '不可用' }}</span>
          <button class="btn btn-ghost btn-sm" type="button" @click="aria2.openConfig()"><Settings2 />aria2</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="openMerge"><GitMerge />合并 CDK</button>
          <button class="btn btn-ghost btn-sm" type="button" @click="toggleHistory"><History />解析历史</button>
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
        <ResolveForm :loading="submitting" @submit="onSubmit" />
        <div class="dock-wrap"><JobStatus :job="job" :phase="phase" :error="error" :submitting="submitting" /></div>
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
/* gate */
.gate-wrap { position: relative; z-index: 1; min-height: 100vh; display: grid; place-items: center; padding: 24px; }
.gate-card { position: relative; width: min(100%, 372px); padding: 30px 28px 24px; text-align: center; overflow: hidden; }
.wire { position: absolute; left: 0; right: 0; top: 0; height: 2px; background: var(--canvas-2); overflow: hidden; }
.wire-pulse { position: absolute; top: 0; left: 0; width: 36%; height: 100%; background: linear-gradient(90deg, transparent, var(--brand), transparent); animation: wireRun 2.6s var(--ease) infinite; }
.mark { width: 46px; height: 46px; margin: 4px auto 14px; display: grid; place-items: center; border-radius: var(--r-lg); background: var(--brand); color: var(--ink-on); box-shadow: var(--shadow-sm); }
.mark svg { width: 22px; height: 22px; }
.gate-card h1 { font-size: var(--fs-xl); font-weight: var(--fw-bold); }
.lede { color: var(--ink-2); font-size: var(--fs-sm); margin-top: 5px; margin-bottom: 18px; }
.cdk-form { display: grid; gap: 11px; text-align: left; }
.cdk-input { height: 40px; text-align: center; text-transform: uppercase; letter-spacing: 0.14em; font-size: var(--fs-md); }

/* portal */
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
.merge-overlay { z-index: 7950; }
.merge-dialog { width: min(430px, calc(100vw - 32px)); }
.merge-dialog-head { margin-bottom: 14px; }
.merge-dialog-head h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.merge-dialog-head h2 svg { width: 17px; height: 17px; color: var(--brand); }
.merge-form { display: grid; gap: 10px; }
.merge-form .input { height: 36px; text-transform: uppercase; }
.merge-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; margin-top: 14px; }
.history-overlay { z-index: 7900; align-items: start; padding-top: 42px; }
.history-dialog {
  width: min(100%, 1180px);
  max-height: calc(100vh - 84px);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.history-dialog-head { flex: none; margin-bottom: 12px; }
.history-dialog-head h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.history-dialog-head h2 svg { width: 17px; height: 17px; color: var(--brand); }
.history-dialog-actions { display: flex; align-items: center; justify-content: flex-end; gap: 8px; flex-wrap: wrap; }
.history-dialog-body { min-height: 0; overflow-y: auto; padding-right: 2px; }
.history-list { display: grid; gap: 9px; }
.history-item {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 13px;
  border: 1px solid var(--line);
  border-radius: var(--r-md);
  background: var(--panel-2);
  color: inherit;
  text-align: left;
  cursor: pointer;
  transition: transform var(--t) var(--ease), box-shadow var(--t) var(--ease), border-color var(--t) var(--ease);
}
.history-item:hover { transform: translateY(-1px); box-shadow: var(--shadow-sm); border-color: var(--brand-soft); }
.history-main { min-width: 0; display: flex; flex-direction: column; gap: 3px; }
.history-title { font-size: var(--fs-sm); font-weight: var(--fw-semi); line-height: 1.45; word-break: break-all; }
.history-sub { font-size: var(--fs-2xs); color: var(--ink-3); }
.history-tags { flex: none; display: flex; align-items: center; justify-content: flex-end; gap: 6px; flex-wrap: wrap; }
.history-detail-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 10px; }
.history-detail-head h3 { font-size: var(--fs-md); font-weight: var(--fw-semi); }
.history-metrics { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.history-input {
  margin: 0 0 14px;
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: var(--r-md);
  background: var(--panel-2);
  color: var(--ink-2);
  font-size: var(--fs-xs);
  white-space: pre-wrap;
  word-break: break-all;
}
.history-state { padding: 16px 0; color: var(--ink-3); font-size: var(--fs-xs); }
.history-empty { min-height: 120px; }

@keyframes wireRun {
  0% { left: -36%; }
  100% { left: 100%; }
}

@media (max-width: 600px) {
  .portal { padding: 16px 14px 48px; }
  .merge-overlay { padding: 12px; }
  .merge-dialog { width: 100%; }
  .merge-actions { justify-content: stretch; }
  .merge-actions .btn { flex: 1 1 0; }
  .history-overlay { align-items: stretch; padding: 12px; }
  .history-dialog { max-height: calc(100vh - 24px); }
  .history-dialog-head { align-items: flex-start; }
  .history-dialog-actions { width: 100%; justify-content: flex-start; }
  .history-item { align-items: flex-start; flex-direction: column; }
  .history-tags { justify-content: flex-start; }
}
</style>
