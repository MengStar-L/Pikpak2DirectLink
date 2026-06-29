<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { UserPlus, Users, Inbox } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import AccountCard from '../AccountCard.vue'
import Skeleton from '../Skeleton.vue'
import { api } from '../../lib/api'
import { toast } from '../../composables/useToast'
import type { AccountSummary } from '../../lib/types'

const emit = defineEmits<{ (e: 'changed'): void }>()

const accounts = ref<AccountSummary[]>([])
const loading = ref(true)
const busyId = ref('')

const form = ref({ username: '', password: '', traffic_limit_gb: 700 })
const formErr = ref('')
const adding = ref(false)

async function load() {
  loading.value = true
  try {
    const { accounts: list } = await api.accounts.list()
    accounts.value = list
  } catch (e: any) {
    toast(e?.message || '加载账号失败', 'error')
  } finally {
    loading.value = false
  }
}

async function addAccount() {
  formErr.value = ''
  if (!form.value.username || !form.value.password) {
    formErr.value = '请填写用户名和密码'
    return
  }
  adding.value = true
  try {
    await api.accounts.add({
      username: form.value.username,
      password: form.value.password,
      traffic_limit_gb: form.value.traffic_limit_gb || 700,
    })
    form.value = { username: '', password: '', traffic_limit_gb: 700 }
    toast('账号已添加', 'success')
    await load()
    emit('changed')
  } catch (e: any) {
    formErr.value = e?.message || '添加失败'
  } finally {
    adding.value = false
  }
}

async function action(id: string, kind: 'update' | 'delete' | 'reset' | 'refresh' | 'delete-parse-error', payload?: number) {
  busyId.value = id
  try {
    if (kind === 'update' && payload !== undefined) await api.accounts.update(id, payload)
    else if (kind === 'delete') await api.accounts.remove(id)
    else if (kind === 'reset') await api.accounts.reset(id)
    else if (kind === 'refresh') await api.accounts.refreshLogin(id)
    else if (kind === 'delete-parse-error' && payload !== undefined) await api.accounts.deleteParseError(id, payload)
    toast(
      kind === 'delete' ? '已删除账号' : kind === 'reset' ? '已重置状态' : kind === 'refresh' ? '已重新登录' : kind === 'delete-parse-error' ? '已删除解析错误' : '已更新流量上限',
      'success',
    )
    await load()
    emit('changed')
  } catch (e: any) {
    toast(e?.message || '操作失败', 'error')
  } finally {
    busyId.value = ''
  }
}

onMounted(load)
defineExpose({ refresh: load })
</script>

<template>
  <div class="page">
    <GlassCard seam>
      <div class="sec-head mb">
        <div class="sec-title">
          <span class="sec-glyph"><Users /></span>
          <div><span class="eyebrow">accounts</span><h2>添加 PikPak 账号</h2><p>账号保存在本地数据目录，用于解析与刷新 session</p></div>
        </div>
      </div>
      <form class="add-form" @submit.prevent="addAccount">
        <label class="field"><span class="field-label">邮箱 / 手机号 / 用户名</span><input v-model="form.username" class="input" type="text" autocomplete="username" /></label>
        <label class="field"><span class="field-label">PikPak 密码</span><input v-model="form.password" class="input" type="password" autocomplete="current-password" /></label>
        <label class="field"><span class="field-label">最大流量 (GB)</span><input v-model.number="form.traffic_limit_gb" class="input input-mono" type="number" min="1" step="1" inputmode="numeric" /></label>
        <PrimaryButton type="submit" :loading="adding"><template #icon><UserPlus /></template>添加并登录</PrimaryButton>
      </form>
      <Transition name="v-fade"><p v-if="formErr" class="error-block">{{ formErr }}</p></Transition>
    </GlassCard>

    <div class="acct-list">
      <template v-if="loading">
        <Skeleton v-for="i in 2" :key="i" height="168px" radius="var(--r-lg)" />
      </template>
      <template v-else-if="accounts.length">
        <AccountCard
          v-for="a in accounts"
          :key="a.id"
          :account="a"
          :busy="busyId === a.id"
          @update="(id, gb) => action(id, 'update', gb)"
          @delete="(id) => action(id, 'delete')"
          @reset="(id) => action(id, 'reset')"
          @refresh="(id) => action(id, 'refresh')"
          @delete-parse-error="(id, index) => action(id, 'delete-parse-error', index)"
        />
      </template>
      <GlassCard v-else>
        <div class="empty"><Inbox /><p>还没有账号，添加一个开始解析。</p></div>
      </GlassCard>
    </div>
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 14px; }
.sec-head.mb { margin-bottom: 14px; }
.eyebrow { display: block; margin-bottom: 2px; }
.add-form { display: grid; grid-template-columns: 1fr 1fr 130px auto; gap: 10px; align-items: end; }
.add-form .btn { height: 34px; }
.acct-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 12px; }
@media (max-width: 820px) { .add-form { grid-template-columns: 1fr; } }
</style>
