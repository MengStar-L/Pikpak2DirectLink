<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import {
  Ticket, LogOut, Gauge, CalendarClock, Hourglass, Link2, Files, CheckCheck, Settings2, Send, Radar, Waypoints,
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
import type { JobResult, UserStatusResponse } from './lib/types'

const view = ref<'gate' | 'portal'>('gate')
const status = ref<UserStatusResponse | null>(null)
const cdkInput = ref('')
const cdkError = ref('')
const cdkLoading = ref(false)
const pushChoiceOpen = ref(false)
const pushChoiceResults = ref<JobResult[]>([])

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

setUnauthorizedHandler(() => {
  view.value = 'gate'
  status.value = null
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
        <ResolveForm :loading="submitting" :allow-proxy="status?.allow_proxy ?? false" @submit="onSubmit" />
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
.dock-wrap { padding-top: 13px; border-top: 1px solid var(--line); }
.res-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }

@keyframes wireRun {
  0% { left: -36%; }
  100% { left: 100%; }
}

@media (max-width: 600px) {
  .portal { padding: 16px 14px 48px; }
}
</style>
