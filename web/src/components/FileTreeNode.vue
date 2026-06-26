<script setup lang="ts">
import { ref, computed } from 'vue'
import { ChevronRight, Folder, File as FileIcon, Check, Minus } from 'lucide-vue-next'
import FileTreeNode from './FileTreeNode.vue'
import type { TreeNode } from '../lib/tree'
import { checkState, toggleNode } from '../lib/tree'
import { formatSize } from '../lib/format'

const props = defineProps<{
  node: TreeNode
  selected: Set<string>
  level: number
}>()

const emit = defineEmits<{ (e: 'toggle'): void }>()

const open = ref(props.level < 1)
const hasChildren = computed(() => props.node.children.length > 0)
const state = computed(() => checkState(props.node, props.selected))
const isFolder = computed(
  () => !props.node.item || props.node.item.kind === 'folder' || hasChildren.value,
)

function onToggle() {
  toggleNode(props.node, props.selected)
  emit('toggle')
}
</script>

<template>
  <div class="tnode">
    <div class="row" :style="{ paddingLeft: 6 + level * 16 + 'px' }">
      <button
        v-if="hasChildren"
        class="caret"
        type="button"
        :aria-label="open ? '折叠' : '展开'"
        @click="open = !open"
      >
        <ChevronRight :class="{ open }" />
      </button>
      <span v-else class="caret-spacer" />

      <button class="cbx" type="button" :class="state" @click="onToggle" aria-label="选择">
        <Check v-if="state === 'checked'" class="pop" />
        <Minus v-else-if="state === 'indeterminate'" />
      </button>

      <Folder v-if="isFolder" class="ficon folder" />
      <FileIcon v-else class="ficon file" />

      <span class="name" :title="node.name">{{ node.name }}</span>
      <span v-if="node.item?.size" class="size mono">{{ formatSize(node.item.size) }}</span>
    </div>

    <div class="children" :class="{ open }">
      <div class="children-inner">
        <FileTreeNode
          v-for="child in node.children"
          :key="child.path"
          :node="child"
          :selected="selected"
          :level="level + 1"
          @toggle="emit('toggle')"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.row {
  display: flex; align-items: center; gap: 7px;
  padding: 5px 8px; border-radius: var(--r-sm);
  transition: background var(--t-fast) var(--ease);
}
.row:hover { background: var(--panel-3); }
.caret {
  display: grid; place-items: center; width: 18px; height: 18px;
  border-radius: var(--r-xs); color: var(--ink-3);
  transition: background var(--t-fast) var(--ease), color var(--t) var(--ease);
}
.caret:hover { color: var(--brand); background: var(--brand-soft); }
.caret svg { width: 15px; height: 15px; transition: transform var(--t) var(--spring); }
.caret svg.open { transform: rotate(90deg); }
.caret-spacer { width: 18px; flex: none; }

.cbx {
  display: grid; place-items: center; width: 18px; height: 18px;
  border: 1.5px solid var(--line-2); border-radius: var(--r-xs); background: var(--panel);
  transition: background var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease);
}
.cbx:hover { border-color: var(--brand); }
.cbx.checked, .cbx.indeterminate { background: var(--brand); border-color: var(--brand); }
.cbx svg { width: 12px; height: 12px; color: var(--ink-on); }
.cbx svg.pop { animation: pop-in var(--t) var(--spring); }

.ficon { width: 15px; height: 15px; flex: none; }
.ficon.folder { color: var(--ink-3); }
.ficon.file { color: var(--brand); }
.name { flex: 1 1 auto; min-width: 0; font-size: var(--fs-sm); color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.size { font-size: var(--fs-xs); color: var(--ink-3); }

.children {
  display: grid;
  grid-template-rows: 0fr;
  transition: grid-template-rows var(--t) var(--ease-out);
}
.children.open { grid-template-rows: 1fr; }
.children-inner { overflow: hidden; min-height: 0; }
</style>
