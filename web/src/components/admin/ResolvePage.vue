<script setup lang="ts">
import { ref, computed } from 'vue'
import { Link2, Files, CheckCheck, Settings2, Send } from 'lucide-vue-next'
import GlassCard from '../GlassCard.vue'
import PrimaryButton from '../PrimaryButton.vue'
import ResolveForm from '../ResolveForm.vue'
import JobStatus from '../JobStatus.vue'
import FileTree from '../FileTree.vue'
import ResultList from '../ResultList.vue'
import { api } from '../../lib/api'
import { useJob } from '../../composables/useJob'
import { aria2 } from '../../composables/useAria2'
import { toast } from '../../composables/useToast'

const { job, phase, error, submitting, submit, selectItems } = useJob({
  create: (b) => api.jobs.create(b),
  get: (id) => api.jobs.get(id),
  select: (id, b) => api.jobs.select(id, b),
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

function onSubmit(payload: { input: string; passCode: string; mode: 'direct' | 'proxy' }) {
  selectedIds.value = []
  submit(payload.input, payload.passCode, payload.mode)
}
function confirmSelection() {
  if (!selectedIds.value.length) { toast('请至少选择一个文件', 'error'); return }
  selectItems(selectedIds.value)
}
function onPush(p: { url: string; name: string }) {
  aria2.pushOne(p.url, p.name)
}
function pushAll() {
  aria2.pushMany(results.value.map((r) => ({ url: r.url || r.direct_url || r.proxy_url, name: r.file.name })))
}
</script>

<template>
  <div class="page">
    <GlassCard class="workbench" seam>
      <div class="sec-head">
        <div class="sec-title">
          <span class="sec-glyph"><Link2 /></span>
          <div><span class="eyebrow">resolve</span><h2>链接解析</h2><p>粘贴磁力或 PikPak 分享链接，生成直链或代理链接</p></div>
        </div>
      </div>
      <ResolveForm :loading="submitting" @submit="onSubmit" />
      <div class="dock-wrap"><JobStatus :job="job" :phase="phase" :error="error" :submitting="submitting" show-attempts /></div>
    </GlassCard>

    <Transition name="v-rise">
      <GlassCard v-if="needSelection" :key="'sel'">
        <div class="sec-head mb">
          <div class="sec-title">
            <span class="sec-glyph live"><Files /></span>
            <div><span class="eyebrow">select</span><h2>选择文件</h2></div>
          </div>
          <PrimaryButton size="sm" :disabled="!selectedIds.length" @click="confirmSelection">
            <template #icon><CheckCheck /></template>解析已选 {{ selectedIds.length }}
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
            <div><span class="eyebrow">output · {{ results.length }}</span><h2>解析结果</h2></div>
          </div>
          <div class="res-actions">
            <button class="link-btn" type="button" @click="aria2.openConfig()"><Settings2 />aria2</button>
            <PrimaryButton v-if="results.length > 1" variant="soft" size="sm" @click="pushAll"><template #icon><Send /></template>全部推送</PrimaryButton>
          </div>
        </div>
        <ResultList :results="results" show-push @push="onPush" />
      </GlassCard>
    </Transition>
  </div>
</template>

<style scoped>
.page { display: flex; flex-direction: column; gap: 14px; }
.sec-glyph.ok { background: var(--ok-soft); color: var(--ok); }
.sec-glyph.live { background: var(--live-soft); color: var(--live); }
.sec-head.mb { margin-bottom: 14px; }
.dock-wrap { padding-top: 13px; margin-top: 14px; border-top: 1px solid var(--line); }
.res-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.eyebrow { display: block; margin-bottom: 2px; }
</style>
