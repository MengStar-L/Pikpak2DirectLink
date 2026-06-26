<script setup lang="ts">
import { ref, watch } from 'vue'
import { Settings2, X, Plug, Save } from 'lucide-vue-next'
import PrimaryButton from './PrimaryButton.vue'
import { aria2, type Aria2Config } from '../composables/useAria2'
import { toast } from '../composables/useToast'

const cfg = ref<Aria2Config>({ rpcUrl: '', token: '', dir: '' })
const err = ref('')
const testing = ref(false)

watch(
  () => aria2.configOpen.value,
  (open) => {
    if (open) {
      cfg.value = { ...aria2.loadConfig() }
      err.value = ''
    }
  },
)

async function test() {
  err.value = ''
  const saved = aria2.saveConfig(cfg.value)
  if (!saved.rpcUrl) {
    err.value = '请填写 RPC 地址'
    return
  }
  testing.value = true
  try {
    const version = await aria2.rpc('aria2.getVersion', [], saved)
    toast('连接成功：aria2 ' + (version?.version || ''), 'success')
  } catch (e: any) {
    err.value = e?.message || '连接失败'
  } finally {
    testing.value = false
  }
}

function save() {
  if (!String(cfg.value.rpcUrl || '').trim()) {
    err.value = '请填写 RPC 地址'
    return
  }
  aria2.saveConfig(cfg.value)
  aria2.closeConfig()
  toast('aria2 配置已保存', 'success')
}
</script>

<template>
  <Teleport to="body">
    <Transition name="v-veil">
      <div v-if="aria2.configOpen.value" class="overlay" @click.self="aria2.closeConfig()">
        <Transition name="v-pop" appear>
          <div v-if="aria2.configOpen.value" class="dialog" role="dialog" aria-modal="true" aria-label="aria2 配置">
            <div class="dialog-head">
              <h2><Settings2 />aria2 推送配置</h2>
              <button type="button" class="dialog-close" aria-label="关闭" @click="aria2.closeConfig()"><X /></button>
            </div>
            <p class="hint">配置仅保存在本浏览器。aria2 需开启 RPC 并允许跨域：<code class="mono">--enable-rpc --rpc-allow-origin-all</code></p>

            <div class="fields">
              <label class="field">
                <span class="field-label">RPC 地址</span>
                <input v-model="cfg.rpcUrl" class="input input-mono" type="text" autocomplete="off" placeholder="http://localhost:6800/jsonrpc" />
              </label>
              <label class="field">
                <span class="field-label">RPC 密钥 Secret（可选）</span>
                <input v-model="cfg.token" class="input input-mono" type="text" autocomplete="off" placeholder="——" />
              </label>
              <label class="field">
                <span class="field-label">下载目录（可选）</span>
                <input v-model="cfg.dir" class="input input-mono" type="text" autocomplete="off" placeholder="留空用 aria2 默认" />
              </label>
            </div>

            <Transition name="v-fade"><p v-if="err" class="error-block">{{ err }}</p></Transition>

            <div class="actions">
              <PrimaryButton variant="line" size="sm" :loading="testing" @click="test"><template #icon><Plug /></template>测试连接</PrimaryButton>
              <PrimaryButton size="sm" @click="save"><template #icon><Save /></template>保存</PrimaryButton>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.dialog-head h2 { display: flex; align-items: center; gap: 7px; font-size: var(--fs-lg); }
.dialog-head h2 svg { width: 17px; height: 17px; color: var(--brand); }
.hint { font-size: var(--fs-xs); color: var(--ink-3); line-height: 1.55; margin-bottom: 14px; }
.fields { display: flex; flex-direction: column; gap: 11px; }
.actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
</style>
