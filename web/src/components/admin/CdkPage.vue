<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Ticket, ExternalLink, Plus, Inbox } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import CdkCard from '../CdkCard.vue'
import Skeleton from '../Skeleton.vue'
import { api } from '../../lib/api'
import { copyText } from '../../lib/clipboard'
import { toast } from '../../composables/useToast'
import type { CDKView } from '../../lib/types'

const emit = defineEmits<{ (e: 'open-user', userID: string): void }>()

const cdks = ref<CDKView[]>([])
const loading = ref(true)
const busyCode = ref('')

const form = ref({ count: 1, traffic_gb: 2, days: 30, allow_proxy: true })
const formErr = ref('')
const generating = ref(false)

async function load() {
  loading.value = true
  try {
    const { cdks: list } = await api.cdks.list()
    cdks.value = list
  } catch (e: any) {
    toast(e?.message || '加载 CDK 失败', 'error')
  } finally {
    loading.value = false
  }
}

async function generate() {
  formErr.value = ''
  if (form.value.count < 1 || form.value.traffic_gb < 1 || form.value.days < 1) {
    formErr.value = '请填写有效的数量、流量和天数'
    return
  }
  generating.value = true
  try {
    const { cdks: created } = await api.cdks.create({
      count: form.value.count,
      traffic_gb: form.value.traffic_gb,
      days: form.value.days,
      allow_proxy: form.value.allow_proxy,
    })
    const copied = await copyText(created.map((c) => c.code).join('\n'))
    toast(
      copied ? `已生成 ${created.length} 个 CDK，并已复制到剪贴板` : `已生成 ${created.length} 个 CDK，但自动复制失败，请手动复制`,
      copied ? 'success' : 'info',
    )
    await load()
  } catch (e: any) {
    formErr.value = e?.message || '生成失败'
  } finally {
    generating.value = false
  }
}

async function action(code: string, kind: 'update' | 'delete', payload?: { traffic_gb: number; days: number; allow_proxy: boolean }) {
  busyCode.value = code
  try {
    if (kind === 'update' && payload) await api.cdks.update(code, payload)
    else if (kind === 'delete') await api.cdks.remove(code)
    toast(kind === 'delete' ? '已撤销 CDK' : '凭证已更新', 'success')
    await load()
  } catch (e: any) {
    toast(e?.message || '操作失败', 'error')
  } finally {
    busyCode.value = ''
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
          <span class="sec-glyph"><Ticket /></span>
          <div><span class="eyebrow">cdk</span><h2>CDK 凭证</h2><p>生成核销凭证，权益以用户订阅为准</p></div>
        </div>
        <div class="head-actions">
          <a class="link-btn" href="/u" target="_blank" rel="noreferrer noopener"><ExternalLink />用户入口</a>
        </div>
      </div>
      <form class="gen-form" @submit.prevent="generate">
        <label class="field"><span class="field-label">分发数量</span><input v-model.number="form.count" class="input input-mono" type="number" min="1" max="100" step="1" inputmode="numeric" /></label>
        <label class="field"><span class="field-label">流量额度 (GB)</span><input v-model.number="form.traffic_gb" class="input input-mono" type="number" min="1" step="1" inputmode="numeric" /></label>
        <label class="field"><span class="field-label">到期天数</span><input v-model.number="form.days" class="input input-mono" type="number" min="1" step="1" inputmode="numeric" /></label>
        <label class="check gen-check"><input v-model="form.allow_proxy" type="checkbox" />支持中转下载</label>
        <PrimaryButton type="submit" :loading="generating"><template #icon><Plus /></template>生成 CDK</PrimaryButton>
      </form>
      <Transition name="v-fade"><p v-if="formErr" class="error-block">{{ formErr }}</p></Transition>
    </GlassCard>

    <div class="cdk-list">
      <template v-if="loading">
        <Skeleton v-for="i in 2" :key="i" height="142px" radius="var(--r-lg)" />
      </template>
      <template v-else-if="cdks.length">
        <CdkCard
          v-for="c in cdks"
          :key="c.code"
          :cdk="c"
          :busy="busyCode === c.code"
          @update="(code, gb, days, allowProxy) => action(code, 'update', { traffic_gb: gb, days, allow_proxy: allowProxy })"
          @delete="(code) => action(code, 'delete')"
          @open-user="(userID) => emit('open-user', userID)"
        />
      </template>
      <GlassCard v-else>
        <div class="empty"><Inbox /><p>还没有 CDK，生成一批分发给用户。</p></div>
      </GlassCard>
    </div>
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 14px; }
.sec-head.mb { margin-bottom: 14px; }
.eyebrow { display: block; margin-bottom: 2px; }
.head-actions { display: flex; align-items: center; gap: 8px; }
.gen-form { display: grid; grid-template-columns: 1fr 1fr 1fr auto auto; gap: 10px 14px; align-items: center; }
.gen-form .field { display: flex; align-items: center; gap: 9px; }
.gen-form .field-label { margin-bottom: 0; flex: none; white-space: nowrap; color: var(--ink-2); }
.gen-form .field .input { flex: 1 1 auto; min-width: 0; }
.gen-check { white-space: nowrap; }
.gen-form .btn { height: 34px; }
.cdk-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 12px; }
@media (max-width: 820px) { .gen-form { grid-template-columns: 1fr 1fr; } }
@media (max-width: 560px) { .gen-form { grid-template-columns: 1fr; } }
</style>
