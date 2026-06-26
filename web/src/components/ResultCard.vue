<script setup lang="ts">
import { ExternalLink, Send, FileText, FolderOpen, Clock } from 'lucide-vue-next'
import CopyButton from './CopyButton.vue'
import type { JobResult } from '../lib/types'
import { formatSize, formatRelative } from '../lib/format'

const props = defineProps<{
  result: JobResult
  showPush?: boolean
}>()

const emit = defineEmits<{
  (e: 'push', payload: { url: string; name: string }): void
}>()

const file = props.result.file
const isFolder = file.kind === 'folder'

function selectAll(e: FocusEvent) {
  const t = e.target as HTMLInputElement
  if (t) t.select()
}
</script>

<template>
  <article class="rcard">
    <header class="rc-head">
      <component :is="isFolder ? FolderOpen : FileText" class="ficon" />
      <div class="meta">
        <h3 class="fname" :title="file.name">{{ file.name }}</h3>
        <p class="fpath mono" v-if="file.path">{{ file.path }}</p>
      </div>
      <div class="badges">
        <span v-if="file.size" class="pill">{{ formatSize(file.size) }}</span>
        <span v-if="result.expires_at" class="pill pill-live">
          <Clock />{{ formatRelative(result.expires_at) }}过期
        </span>
      </div>
    </header>

    <div class="links">
      <div v-if="result.direct_url" class="lrow">
        <span class="tag tag-brand">直链</span>
        <input class="input input-mono linput" :value="result.direct_url" readonly @focus="selectAll" />
        <div class="acts">
          <a class="link-btn" :href="result.direct_url" target="_blank" rel="noreferrer noopener">
            <ExternalLink />打开
          </a>
          <CopyButton :text="result.direct_url" size="sm" />
          <button v-if="showPush" class="link-btn" type="button" @click="emit('push', { url: result.direct_url, name: file.name })">
            <Send />推送
          </button>
        </div>
      </div>

      <div v-if="result.proxy_url" class="lrow">
        <span class="tag">代理</span>
        <input class="input input-mono linput" :value="result.proxy_url" readonly @focus="selectAll" />
        <div class="acts">
          <a class="link-btn" :href="result.proxy_url" target="_blank" rel="noreferrer noopener">
            <ExternalLink />打开
          </a>
          <CopyButton :text="result.proxy_url" size="sm" />
          <button v-if="showPush" class="link-btn" type="button" @click="emit('push', { url: result.proxy_url, name: file.name })">
            <Send />推送
          </button>
        </div>
      </div>
    </div>
  </article>
</template>

<style scoped>
.rcard { padding: 13px 14px; border-radius: var(--r-md); background: var(--panel-2); border: 1px solid var(--line); }
.rc-head { display: flex; align-items: flex-start; gap: 10px; margin-bottom: 11px; }
.ficon { width: 18px; height: 18px; color: var(--brand); flex: none; margin-top: 1px; }
.meta { flex: 1 1 auto; min-width: 0; }
.fname { font-size: var(--fs-sm); font-weight: var(--fw-semi); word-break: break-all; line-height: 1.4; }
.fpath { font-size: var(--fs-2xs); color: var(--ink-3); margin-top: 2px; word-break: break-all; }
.badges { display: flex; flex-direction: column; align-items: flex-end; gap: 5px; flex: none; }

.links { display: flex; flex-direction: column; gap: 8px; }
.lrow { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tag { flex: none; }
.linput { flex: 1 1 200px; min-width: 0; height: 30px; font-size: var(--fs-xs); color: var(--ink-2); }
.acts { display: flex; align-items: center; gap: 5px; flex: none; }
</style>
