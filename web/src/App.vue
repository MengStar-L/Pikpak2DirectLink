<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import AuroraBg from './components/AuroraBg.vue'
import ToastHost from './components/ToastHost.vue'
import Aria2ConfigModal from './components/Aria2ConfigModal.vue'
import Aria2PushOverlay from './components/Aria2PushOverlay.vue'
import Sidebar from './components/admin/Sidebar.vue'
import MetricGrid from './components/admin/MetricGrid.vue'
import ResolvePage from './components/admin/ResolvePage.vue'
import AccountsPage from './components/admin/AccountsPage.vue'
import LogsPage from './components/admin/LogsPage.vue'
import CdkPage from './components/admin/CdkPage.vue'
import UpdatePage from './components/admin/UpdatePage.vue'
import SettingsPage from './components/admin/SettingsPage.vue'
import { api, setUnauthorizedHandler } from './lib/api'
import type { ConfigResponse, SettingsResponse } from './lib/types'

const booted = ref(false)
const active = ref('resolve')
const updateAvailable = ref(false)
const passwordFixed = ref(false)

const config = ref<ConfigResponse | null>(null)
const settings = ref<SettingsResponse | null>(null)

const pageMap = {
  resolve: ResolvePage,
  accounts: AccountsPage,
  logs: LogsPage,
  cdk: CdkPage,
  update: UpdatePage,
  settings: SettingsPage,
} as const
const current = computed(() => pageMap[active.value as keyof typeof pageMap])

setUnauthorizedHandler(() => {
  window.location.replace('/')
})

let pollTimer: number | undefined

async function loadConfig() {
  try {
    config.value = await api.config()
    passwordFixed.value = config.value.password_fixed
  } catch { /* unauthorized handler redirects */ }
}
async function loadSettings() {
  try {
    settings.value = await api.settings.get()
  } catch { /* ignore transient */ }
}
async function boot() {
  await Promise.all([loadConfig(), loadSettings()])
  booted.value = true
  pollTimer = window.setInterval(async () => {
    await Promise.all([loadConfig(), loadSettings()])
  }, 5000)
}

async function logout() {
  try { await api.auth.logout() } catch { /* ignore */ }
  window.location.replace('/')
}

onMounted(boot)
onUnmounted(() => { if (pollTimer) clearInterval(pollTimer) })
</script>

<template>
  <AuroraBg subtle />
  <ToastHost />
  <Aria2ConfigModal />
  <Aria2PushOverlay />

  <div v-if="!booted" class="boot">
    <div class="boot-card panel">
      <Loader2 class="spin boot-ico" />
      <div><h2>正在同步</h2><p>读取账号与系统状态…</p></div>
    </div>
  </div>

  <div v-else class="shell">
    <Sidebar v-model:active="active" :update-available="updateAvailable" :logged-in="config?.authenticated" @logout="logout" />

    <main class="workspace">
      <MetricGrid
        :total="config?.account_count ?? 0"
        :available="config?.available_account_count ?? 0"
        :failed="config?.failed_account_count ?? 0"
        :running="settings?.running ?? 0"
        :waiting="settings?.waiting ?? 0"
      />

      <div class="region">
        <Transition name="v-swap" mode="out-in">
          <component
            :is="current"
            :key="active"
            @changed="loadConfig"
            @available="(v: boolean) => (updateAvailable = v)"
            :password-fixed="passwordFixed"
          />
        </Transition>
      </div>
    </main>
  </div>
</template>

<style scoped>
.boot { position: relative; z-index: 1; min-height: 100vh; display: grid; place-items: center; }
.boot-card { display: flex; align-items: center; gap: 13px; padding: 18px 22px; }
.boot-ico { width: 24px; height: 24px; color: var(--brand); }
.boot-card h2 { font-size: var(--fs-md); font-weight: var(--fw-semi); }
.boot-card p { color: var(--ink-3); font-size: var(--fs-xs); margin-top: 1px; }

.shell { position: relative; z-index: 1; display: flex; height: 100vh; overflow: hidden; }
.workspace {
  flex: 1 1 auto; min-width: 0;
  height: 100vh;
  padding: 18px 24px;
  display: flex; flex-direction: column; gap: 14px;
  max-width: calc(100vw - var(--rail-w));
}
/* the page area scrolls; the readout strip stays pinned, and a page can fill
   the height so its bottom lines up with the rail. */
.region { flex: 1 1 auto; min-height: 0; overflow-y: auto; padding-right: 3px; }

@media (max-width: 720px) {
  .shell { height: auto; overflow: visible; }
  .workspace { height: auto; padding: 16px 14px 96px; max-width: 100vw; }
  .region { overflow: visible; }
}
</style>
