<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ShieldCheck, Eye, EyeOff, Info, AlertTriangle, Lock } from 'lucide-vue-next'
import AuroraBg from './components/AuroraBg.vue'
import PrimaryButton from './components/PrimaryButton.vue'
import { api } from './lib/api'

type Mode = 'login' | 'setup'

const subtitle = ref('正在检查访问状态…')
const mode = ref<Mode>('login')
const ready = ref(false)

const password = ref('')
const confirm = ref('')
const showPw = ref(false)
const showConfirm = ref(false)
const loading = ref(false)
const error = ref('')

const STRENGTH_LABEL = ['', '密码强度：弱', '密码强度：一般', '密码强度：良好', '密码强度：强']

const strength = computed(() => {
  const v = password.value
  if (!v) return 0
  let s = 0
  if (v.length >= 6) s++
  if (v.length >= 10) s++
  if (/[a-z]/.test(v) && /[A-Z]/.test(v)) s++
  if (/\d/.test(v) && /[^A-Za-z0-9]/.test(v)) s++
  if (s === 0) s = 1
  return Math.min(s, 4)
})

function applyMode(configured: boolean) {
  mode.value = configured ? 'login' : 'setup'
  if (mode.value === 'setup') {
    subtitle.value = '设置访问密码以保护此工具'
  } else {
    subtitle.value = '输入访问密码后继续'
  }
  ready.value = true
  requestAnimationFrame(() => document.getElementById('gatePassword')?.focus())
}

function showError(msg: string) {
  error.value = msg
}

async function checkStatus() {
  try {
    const data = await api.auth.status()
    if (data.authenticated) {
      window.location.replace('/')
      return
    }
    applyMode(Boolean(data.configured))
  } catch (e) {
    subtitle.value = '无法连接服务，请稍后重试'
    showError(e instanceof Error ? e.message : '未知错误')
    ready.value = true
  }
}

async function submit() {
  error.value = ''
  if (mode.value === 'setup') {
    if (password.value.length < 6) return showError('密码至少需要 6 位。')
    if (password.value !== confirm.value) return showError('两次输入的密码不一致。')
  } else if (!password.value) {
    return showError('请输入访问密码。')
  }

  loading.value = true
  const endpoint = mode.value === 'setup' ? api.auth.setup : api.auth.login
  try {
    await endpoint(password.value)
    window.location.replace('/')
  } catch (e: any) {
    if (e?.status === 409 && mode.value === 'setup') {
      applyMode(true)
      showError('密码已被设置，请直接登录。')
    } else {
      showError(e?.message || '请求失败')
    }
  } finally {
    loading.value = false
  }
}

onMounted(checkStatus)
</script>

<template>
  <AuroraBg />
  <main class="gate-wrap">
    <section class="gate-card panel anim-rise">
      <div class="wire"><span class="wire-pulse" /></div>

      <header class="gate-header">
        <div class="mark"><ShieldCheck /></div>
        <h1>PikPak 直链工具</h1>
        <p class="sub">{{ subtitle }}</p>
      </header>

      <Transition name="v-fade">
        <div v-if="mode === 'setup' && ready" class="hint">
          <Info />
          <span>首次访问，请设置一个管理员密码。该密码用于后续所有访问。</span>
        </div>
      </Transition>

      <form v-if="ready" class="gate-form" autocomplete="off" @submit.prevent="submit">
        <label class="field">
          <span class="field-label">{{ mode === 'setup' ? '设置密码（至少 6 位）' : '访问密码' }}</span>
          <div class="input-wrap">
            <input
              id="gatePassword"
              v-model="password"
              class="input"
              :type="showPw ? 'text' : 'password'"
              :autocomplete="mode === 'setup' ? 'new-password' : 'current-password'"
            />
            <button type="button" class="input-affix" :aria-label="showPw ? '隐藏密码' : '显示密码'" @click="showPw = !showPw">
              <Eye v-if="!showPw" /><EyeOff v-else />
            </button>
          </div>
        </label>

        <Transition name="v-fade">
          <div v-if="mode === 'setup'" class="strength" :data-score="strength">
            <span v-for="i in 4" :key="i" class="bar" />
            <span class="strength-label">{{ STRENGTH_LABEL[strength] }}</span>
          </div>
        </Transition>

        <Transition name="v-fade">
          <label v-if="mode === 'setup'" class="field">
            <span class="field-label">确认密码</span>
            <div class="input-wrap">
              <input v-model="confirm" class="input" :type="showConfirm ? 'text' : 'password'" autocomplete="new-password" />
              <button type="button" class="input-affix" :aria-label="showConfirm ? '隐藏密码' : '显示密码'" @click="showConfirm = !showConfirm">
                <Eye v-if="!showConfirm" /><EyeOff v-else />
              </button>
            </div>
          </label>
        </Transition>

        <PrimaryButton type="submit" block size="lg" :loading="loading">
          <template #icon><Lock /></template>
          {{ mode === 'setup' ? '设置密码并进入' : '解锁进入' }}
        </PrimaryButton>

        <Transition name="v-fade">
          <p v-if="error" class="error-block"><AlertTriangle />{{ error }}</p>
        </Transition>
      </form>

      <div class="gate-foot"><Lock /><span>本地凭据 · 加密存储</span></div>
    </section>
  </main>
</template>

<style scoped>
.gate-wrap { position: relative; z-index: 1; min-height: 100vh; display: grid; place-items: center; padding: 24px; }
.gate-card { position: relative; width: min(100%, 388px); padding: 30px 30px 22px; overflow: hidden; }
.wire { position: absolute; left: 0; right: 0; top: 0; height: 2px; background: var(--canvas-2); overflow: hidden; }
.wire-pulse { position: absolute; top: 0; left: 0; width: 36%; height: 100%; background: linear-gradient(90deg, transparent, var(--brand), transparent); animation: wireRun 2.6s var(--ease) infinite; }

.gate-header { display: grid; gap: 9px; text-align: center; margin-bottom: 20px; }
.gate-header h1 { font-size: var(--fs-xl); font-weight: var(--fw-bold); }
.gate-header .sub { color: var(--ink-2); font-size: var(--fs-sm); min-height: 19px; }
.mark { width: 50px; height: 50px; margin: 4px auto 4px; display: grid; place-items: center; border-radius: var(--r-lg); background: var(--brand); color: var(--ink-on); box-shadow: var(--shadow-sm); }
.mark svg { width: 25px; height: 25px; }

.hint {
  display: flex; align-items: flex-start; gap: 8px;
  margin-bottom: 14px; padding: 10px 12px;
  border-radius: var(--r-md);
  background: var(--brand-soft); border: 1px solid var(--brand-line);
  color: var(--brand-ink); font-size: var(--fs-xs); line-height: 1.55;
}
.hint svg { width: 15px; height: 15px; flex: none; margin-top: 1px; }

.gate-form { display: grid; gap: 12px; }

.strength { display: grid; grid-template-columns: repeat(4, 1fr); align-items: center; gap: 5px; margin: -4px 0 0; }
.strength .bar { height: 4px; border-radius: var(--r-pill); background: var(--line-2); transition: background var(--t) var(--ease); }
.strength .strength-label { grid-column: 1 / -1; margin-top: 1px; font-size: var(--fs-2xs); font-weight: var(--fw-med); color: var(--ink-3); transition: color var(--t) var(--ease); }
.strength[data-score="1"] .bar:nth-child(-n+1) { background: var(--danger); }
.strength[data-score="2"] .bar:nth-child(-n+2) { background: var(--live); }
.strength[data-score="3"] .bar:nth-child(-n+3) { background: var(--info); }
.strength[data-score="4"] .bar:nth-child(-n+4) { background: var(--ok); }
.strength[data-score="1"] .strength-label { color: var(--danger); }
.strength[data-score="2"] .strength-label { color: var(--live-ink); }
.strength[data-score="3"] .strength-label { color: var(--info); }
.strength[data-score="4"] .strength-label { color: var(--ok); }

.gate-foot { margin-top: 18px; display: flex; align-items: center; justify-content: center; gap: 6px; color: var(--ink-3); font-size: var(--fs-2xs); }
.gate-foot svg { width: 12px; height: 12px; }

@keyframes wireRun {
  0% { left: -36%; }
  100% { left: 100%; }
}
</style>
