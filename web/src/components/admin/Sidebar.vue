<script setup lang="ts">
import { Radar, Link2, Users, Terminal, Ticket, Download, Settings, LogOut } from 'lucide-vue-next'

defineProps<{
  active: string
  updateAvailable?: boolean
  loggedIn?: boolean
}>()

const emit = defineEmits<{ (e: 'update:active', page: string): void; (e: 'logout'): void }>()

const nav = [
  { id: 'resolve', icon: Link2, tip: '解析' },
  { id: 'accounts', icon: Users, tip: '账号' },
  { id: 'logs', icon: Terminal, tip: '日志' },
  { id: 'cdk', icon: Ticket, tip: 'CDK' },
  { id: 'update', icon: Download, tip: '更新' },
  { id: 'settings', icon: Settings, tip: '设置' },
] as const
</script>

<template>
  <aside class="rail">
    <div class="brand" title="PikPak 直链工具"><Radar /></div>

    <nav class="nav">
      <button
        v-for="item in nav"
        :key="item.id"
        type="button"
        class="rbtn"
        :class="{ active: active === item.id }"
        :aria-label="item.tip"
        :data-tip="item.tip"
        @click="emit('update:active', item.id)"
      >
        <component :is="item.icon" />
        <span v-if="item.id === 'update' && updateAvailable" class="dot" />
      </button>
    </nav>

    <div class="foot">
      <button v-if="loggedIn" type="button" class="rbtn" aria-label="退出登录" data-tip="退出" @click="emit('logout')">
        <LogOut />
      </button>
    </div>
  </aside>
</template>

<style scoped>
.rail {
  position: sticky; top: 0; align-self: flex-start;
  z-index: 40; /* keep hover tooltips above the workspace panels */
  display: flex; flex-direction: column; align-items: center; gap: 12px;
  padding: 14px 9px;
  height: 100vh;
  width: var(--rail-w);
  flex: none;
  background: var(--panel);
  border-right: 1px solid var(--line);
}
.brand {
  width: 38px; height: 38px; border-radius: var(--r-md);
  display: grid; place-items: center;
  background: var(--brand); color: var(--ink-on);
  box-shadow: var(--shadow-sm);
}
.brand svg { width: 20px; height: 20px; }
.nav { display: flex; flex-direction: column; gap: 4px; flex: 1 1 auto; }
.rbtn {
  position: relative;
  display: grid; place-items: center;
  width: 40px; height: 40px; border-radius: var(--r-md);
  color: var(--ink-3);
  transition: color var(--t) var(--ease), background var(--t) var(--ease);
}
.rbtn svg { width: 19px; height: 19px; }
.rbtn::after {
  content: attr(data-tip);
  position: absolute; left: calc(100% + 10px); top: 50%; transform: translateY(-50%) translateX(-4px);
  padding: 4px 9px; border-radius: var(--r-sm);
  background: var(--ink); color: var(--panel);
  font-size: var(--fs-2xs); font-weight: var(--fw-med); white-space: nowrap;
  opacity: 0; pointer-events: none;
  transition: opacity var(--t) var(--ease), transform var(--t) var(--ease);
  z-index: 5;
}
.rbtn:hover { color: var(--brand); background: var(--brand-soft); }
.rbtn:hover::after { opacity: 1; transform: translateY(-50%) translateX(0); }
.rbtn.active { color: var(--brand); background: var(--brand-soft); }
.rbtn.active::before {
  content: ""; position: absolute; left: -9px; top: 50%; transform: translateY(-50%);
  width: 3px; height: 20px; border-radius: var(--r-pill); background: var(--brand);
}
.dot { position: absolute; top: 8px; right: 8px; width: 7px; height: 7px; border-radius: 50%; background: var(--live); animation: live-pulse 1.8s var(--ease) infinite; }
.foot { display: flex; flex-direction: column; gap: 4px; }

@media (max-width: 720px) {
  .rail {
    width: auto; height: auto;
    position: fixed; top: auto; bottom: 12px; left: 50%; transform: translateX(-50%);
    flex-direction: row; padding: 7px;
    border: 1px solid var(--line); border-radius: var(--r-xl);
    box-shadow: var(--shadow-md); z-index: 50;
  }
  .brand, .foot { display: none; }
  .nav { flex-direction: row; gap: 3px; }
  .rbtn::after, .rbtn.active::before { display: none; }
}
</style>
