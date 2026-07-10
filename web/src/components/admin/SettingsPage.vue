<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { Copy, DatabaseBackup, Gauge, KeyRound, Lock, Save, ShieldAlert, ShieldCheck, Trash2 } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import { api } from '../../lib/api'
import { copyText } from '../../lib/clipboard'
import { toast } from '../../composables/useToast'
import { formatBytes, formatDateTime, formatRelative } from '../../lib/format'
import type { AuthSettingsResponse, SettingsResponse, StorageStatusResponse } from '../../lib/types'

const props = defineProps<{ passwordFixed: boolean }>()

const settings = ref<SettingsResponse | null>(null)
const concInput = ref(1)
const timeoutInput = ref(60)
const concErr = ref('')
const concSaving = ref(false)

const storage = ref<StorageStatusResponse | null>(null)
const storageErr = ref('')
const backupRunning = ref(false)
const migrationDeleting = ref(false)
const migrationDeleteConfirmed = ref(false)
let storageRefreshTimer: number | undefined
let storageMounted = true

const pw = ref({ current: '', new: '', confirm: '' })
const pwErr = ref('')
const pwSaving = ref(false)

const authSettings = ref<AuthSettingsResponse | null>(null)
const authForm = ref({
  linuxdo_client_id: '',
  linuxdo_client_secret: '',
  clear_linuxdo_client_secret: false,
  linuxdo_login_enabled: true,
  linuxdo_registration_enabled: true,
  email_login_enabled: true,
  email_registration_enabled: false,
})
const authErr = ref('')
const authSaving = ref(false)

async function loadSettings() {
  try {
    settings.value = await api.settings.get()
    concInput.value = settings.value.concurrency
    timeoutInput.value = Math.max(1, Math.round(settings.value.task_timeout_seconds / 60))
  } catch (e: any) {
    toast(e?.message || '加载设置失败', 'error')
  }
}

async function loadAuthSettings() {
  try {
    authSettings.value = await api.settings.auth.get()
    authForm.value = {
      linuxdo_client_id: authSettings.value.linuxdo_client_id,
      linuxdo_client_secret: '',
      clear_linuxdo_client_secret: false,
      linuxdo_login_enabled: authSettings.value.linuxdo_login_enabled,
      linuxdo_registration_enabled: authSettings.value.linuxdo_registration_enabled,
      email_login_enabled: authSettings.value.email_login_enabled,
      email_registration_enabled: authSettings.value.email_registration_enabled,
    }
  } catch (e: any) {
    toast(e?.message || '加载用户登录设置失败', 'error')
  }
}

async function loadStorage() {
	clearStorageRefresh()
	storageErr.value = ''
	try {
		const payload = await api.settings.storage.get()
		if (!storageMounted) return
		storage.value = payload
		if (!storage.value.migration?.backup_id) migrationDeleteConfirmed.value = false
		if (storage.value.running) {
			storageRefreshTimer = window.setTimeout(loadStorage, 1500)
		}
	} catch (e: any) {
		storageErr.value = e?.message || '加载存储状态失败'
	}
}

function clearStorageRefresh() {
	if (storageRefreshTimer) {
		window.clearTimeout(storageRefreshTimer)
		storageRefreshTimer = undefined
	}
}

async function runBackup() {
  storageErr.value = ''
  backupRunning.value = true
  try {
    await api.settings.storage.backup()
    toast('数据库备份已创建并校验', 'success')
    await loadStorage()
  } catch (e: any) {
    storageErr.value = e?.message || '创建备份失败'
  } finally {
    backupRunning.value = false
  }
}

async function deleteMigrationBackup() {
  const backupID = storage.value?.migration?.backup_id
  if (!backupID || !migrationDeleteConfirmed.value) return
  storageErr.value = ''
  migrationDeleting.value = true
  try {
    await api.settings.storage.deleteMigrationBackup(backupID)
    migrationDeleteConfirmed.value = false
    toast('旧版迁移备份已删除', 'success')
    await loadStorage()
  } catch (e: any) {
    storageErr.value = e?.message || '删除迁移备份失败'
  } finally {
    migrationDeleting.value = false
  }
}

function backupStatusLabel(status?: string) {
  if (status === 'success') return '校验通过'
  if (status === 'failed') return '失败'
  if (status === 'running') return '进行中'
  return '尚未执行'
}

function backupIntervalLabel(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '-'
  const hours = seconds / 3600
  return Number.isInteger(hours) ? `${hours} 小时` : `${Math.round(hours * 10) / 10} 小时`
}

async function saveAuthSettings() {
  authErr.value = ''
  authSaving.value = true
  try {
    authSettings.value = await api.settings.auth.update({
      linuxdo_client_id: authForm.value.linuxdo_client_id,
      linuxdo_client_secret: authForm.value.linuxdo_client_secret || undefined,
      clear_linuxdo_client_secret: authForm.value.clear_linuxdo_client_secret,
      linuxdo_login_enabled: authForm.value.linuxdo_login_enabled,
      linuxdo_registration_enabled: authForm.value.linuxdo_registration_enabled,
      email_login_enabled: authForm.value.email_login_enabled,
      email_registration_enabled: authForm.value.email_registration_enabled,
    })
    authForm.value.linuxdo_client_secret = ''
    authForm.value.clear_linuxdo_client_secret = false
    toast('用户登录设置已保存', 'success')
    await loadAuthSettings()
  } catch (e: any) {
    authErr.value = e?.message || '保存失败'
  } finally {
    authSaving.value = false
  }
}

async function copyCallbackURL() {
  if (!authSettings.value?.linuxdo_callback_url) return
  toast(await copyText(authSettings.value.linuxdo_callback_url) ? '回调地址已复制' : '复制失败', 'info')
}

async function saveSettings() {
  concErr.value = ''
  if (!Number.isFinite(concInput.value) || concInput.value < 1 || concInput.value > 32) { concErr.value = '并发数需在 1–32 之间'; return }
  if (!Number.isFinite(timeoutInput.value) || timeoutInput.value < 1) { concErr.value = '超时至少 1 分钟'; return }
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

onMounted(async () => {
	await Promise.all([loadSettings(), loadAuthSettings(), loadStorage()])
})

onUnmounted(() => {
	storageMounted = false
	clearStorageRefresh()
})
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
          <span class="sec-glyph ok"><DatabaseBackup /></span>
          <div><span class="eyebrow">storage</span><h2>数据库备份</h2><p>查看自动备份状态，或立即创建一份经过完整性校验的 SQLite 快照</p></div>
        </div>
        <PrimaryButton size="sm" variant="soft" :loading="backupRunning || storage?.running" @click="runBackup">
          <template #icon><DatabaseBackup /></template>立即备份
        </PrimaryButton>
      </div>

      <div class="storage-grid">
        <div class="storage-row"><span>备份目录</span><b class="mono path-value">{{ storage?.backup_dir || '-' }}</b></div>
        <div class="storage-row"><span>自动备份</span><b>{{ backupIntervalLabel(storage?.backup_interval_seconds ?? 0) }} · 保留 {{ storage?.backup_retention ?? '-' }} 份</b></div>
        <div class="storage-row"><span>最近一次</span><b :class="{ 'danger-text': storage?.last_run?.status === 'failed' }">{{ backupStatusLabel(storage?.last_run?.status) }} · {{ formatDateTime(storage?.last_run?.completed_at || storage?.last_run?.started_at || '') }}</b></div>
        <div class="storage-row"><span>最近成功</span><b>{{ storage?.last_success ? `${formatBytes(storage.last_success.size_bytes)} · ${formatDateTime(storage.last_success.completed_at || storage.last_success.started_at)}` : '暂无' }}</b></div>
        <div class="storage-row"><span>下次执行</span><b>{{ storage?.next_run_at ? formatRelative(storage.next_run_at) : '-' }}</b></div>
      </div>

      <div v-if="storage?.migration?.backup_id" class="migration-block">
        <div class="migration-copy">
          <ShieldAlert />
          <div>
            <strong>旧版迁移备份待确认</strong>
            <p>确认新数据库可正常使用后，才可删除这份可能含明文凭据的升级备份。</p>
            <code class="mono">{{ storage.migration.backup_path || storage.migration.backup_id }}</code>
          </div>
        </div>
        <div class="migration-actions">
          <label class="check danger"><input v-model="migrationDeleteConfirmed" type="checkbox" />我已验证迁移结果与当前数据库</label>
          <PrimaryButton size="sm" variant="danger" :disabled="!migrationDeleteConfirmed" :loading="migrationDeleting" @click="deleteMigrationBackup">
            <template #icon><Trash2 /></template>删除迁移备份
          </PrimaryButton>
        </div>
      </div>
      <p v-else-if="storage" class="fixed-note storage-ok"><ShieldCheck /><span>当前没有待确认的旧版迁移备份。</span></p>
      <Transition name="v-fade"><p v-if="storageErr" class="error-block">{{ storageErr }}</p></Transition>
    </GlassCard>

    <GlassCard seam>
      <div class="sec-head mb">
        <div class="sec-title">
          <span class="sec-glyph ok"><ShieldCheck /></span>
          <div><span class="eyebrow">user auth</span><h2>用户登录</h2><p>配置 LinuxDo 登录、社区用户注册和邮箱注册入口</p></div>
        </div>
      </div>
      <form class="auth-form" @submit.prevent="saveAuthSettings">
        <label class="field callback-field">
          <span class="field-label">LinuxDo 回调地址</span>
          <div class="copy-row">
            <input class="input input-mono" :value="authSettings?.linuxdo_callback_url || '-'" readonly />
            <button class="btn btn-line btn-sm" type="button" @click="copyCallbackURL"><Copy />复制</button>
          </div>
        </label>
        <label class="field"><span class="field-label">Client ID</span><input v-model="authForm.linuxdo_client_id" class="input input-mono" type="text" autocomplete="off" /></label>
        <label class="field"><span class="field-label">Client Secret</span><input v-model="authForm.linuxdo_client_secret" class="input input-mono" type="password" autocomplete="new-password" placeholder="留空则不修改" /></label>
        <div class="check-grid">
          <label class="check"><input v-model="authForm.linuxdo_login_enabled" type="checkbox" />LinuxDo 登录</label>
          <label class="check"><input v-model="authForm.linuxdo_registration_enabled" type="checkbox" />LinuxDo 注册</label>
          <label class="check"><input v-model="authForm.email_login_enabled" type="checkbox" />邮箱登录</label>
          <label class="check"><input v-model="authForm.email_registration_enabled" type="checkbox" />邮箱注册</label>
          <label class="check danger"><input v-model="authForm.clear_linuxdo_client_secret" type="checkbox" />清空 Secret</label>
        </div>
        <PrimaryButton type="submit" :loading="authSaving"><template #icon><Save /></template>保存登录设置</PrimaryButton>
      </form>
      <p class="state">
        LinuxDo <b class="mono">{{ authSettings?.linuxdo_configured ? '已配置' : '未配置' }}</b> ·
        Secret <b class="mono">{{ authSettings?.linuxdo_client_secret_configured ? '已保存' : '未保存' }}</b> ·
        邮箱注册 <b class="mono">{{ authSettings?.email_registration_enabled ? '开启' : '关闭' }}</b>
      </p>
      <Transition name="v-fade"><p v-if="authErr" class="error-block">{{ authErr }}</p></Transition>
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
.auth-form { display: grid; grid-template-columns: 1.4fr 1fr 1fr; gap: 10px; align-items: end; }
.auth-form .btn { height: 34px; }
.callback-field { grid-column: 1 / -1; }
.copy-row { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; align-items: center; }
.check-grid { grid-column: 1 / -1; display: flex; flex-wrap: wrap; gap: 10px 16px; align-items: center; padding: 3px 0; }
.check.danger { color: var(--danger-ink); }
.state { font-size: var(--fs-sm); color: var(--ink-2); margin-top: 12px; }
.state b { color: var(--ink); font-weight: var(--fw-semi); }
.storage-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); border-top: 1px solid var(--line); }
.storage-row { min-width: 0; display: flex; align-items: baseline; justify-content: space-between; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--line); font-size: var(--fs-sm); }
.storage-row:nth-child(odd) { padding-right: 18px; }
.storage-row:nth-child(even) { padding-left: 18px; border-left: 1px solid var(--line); }
.storage-row span { flex: none; color: var(--ink-3); }
.storage-row b { min-width: 0; color: var(--ink); font-weight: var(--fw-semi); text-align: right; overflow-wrap: anywhere; }
.storage-row .path-value { font-size: var(--fs-xs); }
.storage-row .danger-text { color: var(--danger-ink); }
.migration-block { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 18px; margin-top: 14px; padding-top: 14px; border-top: 1px solid var(--danger-line); }
.migration-copy { min-width: 0; display: flex; align-items: flex-start; gap: 10px; color: var(--danger-ink); }
.migration-copy > svg { width: 18px; height: 18px; flex: none; margin-top: 1px; }
.migration-copy strong { display: block; font-size: var(--fs-sm); }
.migration-copy p { margin: 3px 0 6px; color: var(--ink-2); font-size: var(--fs-xs); line-height: 1.5; }
.migration-copy code { display: block; max-width: 100%; color: var(--ink-3); font-size: var(--fs-2xs); overflow-wrap: anywhere; }
.migration-actions { display: flex; flex-direction: column; align-items: flex-end; gap: 9px; }
.migration-actions .check { font-size: var(--fs-xs); }
.storage-ok { margin-top: 14px; }
.fixed-note { display: flex; align-items: flex-start; gap: 8px; font-size: var(--fs-sm); color: var(--ink-2); padding: 11px 13px; border-radius: var(--r-md); background: var(--live-soft); border: 1px solid var(--live-line); line-height: 1.55; }
.fixed-note svg { width: 15px; height: 15px; flex: none; margin-top: 2px; color: var(--live-ink); }
.fixed-note code { font-size: var(--fs-xs); }
@media (max-width: 820px) {
  .form, .form.pw, .auth-form, .storage-grid { grid-template-columns: 1fr; }
  .callback-field, .check-grid { grid-column: auto; }
  .storage-row:nth-child(odd), .storage-row:nth-child(even) { padding-left: 0; padding-right: 0; border-left: 0; }
  .migration-block { grid-template-columns: 1fr; }
  .migration-actions { align-items: stretch; }
}
</style>
