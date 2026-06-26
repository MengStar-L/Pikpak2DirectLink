<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Gauge, Lock, Save, KeyRound } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import { api } from '../../lib/api'
import { toast } from '../../composables/useToast'
import type { SettingsResponse } from '../../lib/types'

const props = defineProps<{ passwordFixed: boolean }>()

const settings = ref<SettingsResponse | null>(null)
const concInput = ref(1)
const timeoutInput = ref(60)
const concErr = ref('')
const concSaving = ref(false)

const pw = ref({ current: '', new: '', confirm: '' })
const pwErr = ref('')
const pwSaving = ref(false)

async function loadSettings() {
  try {
    settings.value = await api.settings.get()
    concInput.value = settings.value.concurrency
    timeoutInput.value = Math.max(1, Math.round(settings.value.task_timeout_seconds / 60))
  } catch (e: any) {
    toast(e?.message || '加载设置失败', 'error')
  }
}

async function saveSettings() {
  concErr.value = ''
  if (concInput.value < 1 || concInput.value > 32) { concErr.value = '并发数需在 1–32 之间'; return }
  if (timeoutInput.value < 1) { concErr.value = '超时至少 1 分钟'; return }
  concSaving.value = true
  try {
    settings.value = await api.settings.update({ concurrency: concInput.value, task_timeout_minutes: timeoutInput.value })
    toast('设置已保存', 'success')
  } catch (e: any) {
    concErr.value = e?.message || '保存失败'
  } finally {
    concSaving.value = false
  }
}

async function changePassword() {
  pwErr.value = ''
  if (pw.value.new.length < 6) { pwErr.value = '新密码至少 6 位'; return }
  if (pw.value.new !== pw.value.confirm) { pwErr.value = '两次输入不一致'; return }
  pwSaving.value = true
  try {
    await api.auth.password(pw.value.current, pw.value.new)
    pw.value = { current: '', new: '', confirm: '' }
    toast('密码已修改，其它设备需重新登录', 'success')
  } catch (e: any) {
    pwErr.value = e?.message || '修改失败'
  } finally {
    pwSaving.value = false
  }
}

onMounted(loadSettings)
</script>

<template>
  <div class="page">
    <GlassCard seam>
      <div class="sec-head mb">
        <div class="sec-title">
          <span class="sec-glyph"><Gauge /></span>
          <div><span class="eyebrow">concurrency</span><h2>并行解析</h2><p>同时解析的任务数与新任务超时。串行（1）逐个处理；并行（&gt;1）可同时处理多个链接</p></div>
        </div>
      </div>
      <form class="form" @submit.prevent="saveSettings">
        <label class="field"><span class="field-label">并发解析数（1 为串行）</span><input v-model.number="concInput" class="input input-mono" type="number" min="1" max="32" step="1" inputmode="numeric" /></label>
        <label class="field"><span class="field-label">任务超时（分钟）</span><input v-model.number="timeoutInput" class="input input-mono" type="number" min="1" max="720" step="1" inputmode="numeric" /></label>
        <PrimaryButton type="submit" :loading="concSaving"><template #icon><Save /></template>保存设置</PrimaryButton>
      </form>
      <p class="state">
        当前 <b class="mono">并发 {{ settings?.concurrency ?? '-' }}</b> ·
        <b class="mono">超时 {{ settings ? Math.round(settings.task_timeout_seconds / 60) : '-' }} 分钟</b> ·
        队列 <b class="mono">{{ settings?.waiting ?? 0 }} 等待 / {{ settings?.running ?? 0 }} 运行</b>
      </p>
      <Transition name="v-fade"><p v-if="concErr" class="error-block">{{ concErr }}</p></Transition>
    </GlassCard>

    <GlassCard seam>
      <div class="sec-head mb">
        <div class="sec-title">
          <span class="sec-glyph live"><Lock /></span>
          <div><span class="eyebrow">access</span><h2>修改访问密码</h2><p>更改登录此工具的访问密码，修改后其它设备需重新登录</p></div>
        </div>
      </div>
      <form v-if="!passwordFixed" class="form pw" @submit.prevent="changePassword">
        <label class="field"><span class="field-label">当前密码</span><input v-model="pw.current" class="input" type="password" autocomplete="current-password" /></label>
        <label class="field"><span class="field-label">新密码（至少 6 位）</span><input v-model="pw.new" class="input" type="password" autocomplete="new-password" /></label>
        <label class="field"><span class="field-label">确认新密码</span><input v-model="pw.confirm" class="input" type="password" autocomplete="new-password" /></label>
        <PrimaryButton type="submit" :loading="pwSaving"><template #icon><KeyRound /></template>修改密码</PrimaryButton>
      </form>
      <p v-else class="fixed-note"><KeyRound /><span>访问密码由 <code class="mono">ACCESS_PASSWORD</code> 环境变量固定，无法在此修改。如需更改，请修改环境变量后重启服务。</span></p>
      <Transition name="v-fade"><p v-if="pwErr" class="error-block">{{ pwErr }}</p></Transition>
    </GlassCard>
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 14px; }
.sec-head.mb { margin-bottom: 14px; }
.eyebrow { display: block; margin-bottom: 2px; }
.form { display: grid; grid-template-columns: 1fr 1fr auto; gap: 10px; align-items: end; }
.form.pw { grid-template-columns: 1fr 1fr 1fr auto; }
.form .btn { height: 34px; }
.state { font-size: var(--fs-sm); color: var(--ink-2); margin-top: 12px; }
.state b { color: var(--ink); font-weight: var(--fw-semi); }
.fixed-note { display: flex; align-items: flex-start; gap: 8px; font-size: var(--fs-sm); color: var(--ink-2); padding: 11px 13px; border-radius: var(--r-md); background: var(--live-soft); border: 1px solid var(--live-line); line-height: 1.55; }
.fixed-note svg { width: 15px; height: 15px; flex: none; margin-top: 2px; color: var(--live-ink); }
.fixed-note code { font-size: var(--fs-xs); }
@media (max-width: 820px) { .form, .form.pw { grid-template-columns: 1fr; } }
</style>
