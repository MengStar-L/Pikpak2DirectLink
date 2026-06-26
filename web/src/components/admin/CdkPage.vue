<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Ticket, ExternalLink, Trash2, Plus, Inbox } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import CdkCard from '../CdkCard.vue'
import Skeleton from '../Skeleton.vue'
import { api } from '../../lib/api'
import { toast } from '../../composables/useToast'
import type { CDKView } from '../../lib/types'

const cdks = ref<CDKView[]>([])
const loading = ref(true)
const busyCode = ref('')

const form = ref({ count: 1, traffic_gb: 2, days: 30 })
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
    })
    toast(`已生成 ${created.length} 个 CDK`, 'success')
    await load()
  } catch (e: any) {
    formErr.value = e?.message || '生成失败'
  } finally {
    generating.value = false
  }
}

async function purgeExpired() {
  try {
    const { deleted } = await api.cdks.deleteExpired()
    toast(deleted ? `已清理 ${deleted} 个过期 CDK` : '没有过期 CDK', 'success')
    await load()
  } catch (e: any) {
    toast(e?.message || '清理失败', 'error')
  }
}

async function action(code: string, kind: 'update' | 'delete', payload?: { traffic_gb: number; days: number }) {
  busyCode.value = code
  try {
    if (kind === 'update' && payload) await api.cdks.update(code, payload)
    else if (kind === 'delete') await api.cdks.remove(code)
    toast(kind === 'delete' ? '已删除 CDK' : '已重置额度', 'success')
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
          <div><span class="eyebrow">cdk</span><h2>CDK 分发</h2><p>生成兑换码，用户凭码进入用户页面使用解析</p></div>
        </div>
        <div class="head-actions">
          <a class="link-btn" href="/u" target="_blank" rel="noreferrer noopener"><ExternalLink />用户入口</a>
          <PrimaryButton variant="line" size="sm" @click="purgeExpired"><template #icon><Trash2 /></template>清理过期</PrimaryButton>
        </div>
      </div>
      <form class="gen-form" @submit.prevent="generate">
        <label class="field"><span class="field-label">分发数量</span><input v-model.number="form.count" class="input input-mono" type="number" min="1" max="100" step="1" inputmode="numeric" /></label>
        <label class="field"><span class="field-label">流量额度 (GB)</span><input v-model.number="form.traffic_gb" class="input input-mono" type="number" min="1" step="1" inputmode="numeric" /></label>
        <label class="field"><span class="field-label">到期天数</span><input v-model.number="form.days" class="input input-mono" type="number" min="1" step="1" inputmode="numeric" /></label>
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
          @update="(code, gb, days) => action(code, 'update', { traffic_gb: gb, days })"
          @delete="(code) => action(code, 'delete')"
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
.gen-form { display: grid; grid-template-columns: 1fr 1fr 1fr auto; gap: 10px 14px; align-items: center; }
.gen-form .field { display: flex; align-items: center; gap: 9px; }
.gen-form .field-label { margin-bottom: 0; flex: none; white-space: nowrap; color: var(--ink-2); }
.gen-form .field .input { flex: 1 1 auto; min-width: 0; }
.gen-form .btn { height: 34px; }
.cdk-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 12px; }
@media (max-width: 820px) { .gen-form { grid-template-columns: 1fr 1fr; } }
@media (max-width: 560px) { .gen-form { grid-template-columns: 1fr; } }
</style>
