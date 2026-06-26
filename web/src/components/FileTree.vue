<script setup lang="ts">
import { computed, watch, ref } from 'vue'
import { Check, Minus } from 'lucide-vue-next'
import FileTreeNode from './FileTreeNode.vue'
import { buildTree, selectableIds, countLeaves, type TreeNode } from '../lib/tree'
import type { DownloadItem } from '../lib/types'

const props = defineProps<{
  items: DownloadItem[]
  modelValue: string[]
  label?: string
}>()

const emit = defineEmits<{ (e: 'update:modelValue', ids: string[]): void }>()

const tree = computed<TreeNode[]>(() => buildTree(props.items))
const selected = ref<Set<string>>(new Set(props.modelValue))

watch(
  () => props.modelValue,
  (ids) => {
    selected.value = new Set(ids)
  },
)

const totalLeaves = computed(() => countLeaves(tree.value))
const allRootIds = computed(() => {
  const ids: string[] = []
  for (const n of tree.value) ids.push(...selectableIds(n))
  return ids
})
const allState = computed(() => {
  if (allRootIds.value.length === 0) return 'unchecked' as const
  const hit = allRootIds.value.filter((id) => selected.value.has(id)).length
  if (hit === 0) return 'unchecked' as const
  if (hit === allRootIds.value.length) return 'checked' as const
  return 'indeterminate' as const
})
const selectedCount = computed(() => selected.value.size)

function emitUpdate() {
  emit('update:modelValue', Array.from(selected.value))
}

function toggleAll() {
  if (allState.value === 'unchecked') {
    allRootIds.value.forEach((id) => selected.value.add(id))
  } else {
    allRootIds.value.forEach((id) => selected.value.delete(id))
  }
  emitUpdate()
}

function onChildToggle() {
  emitUpdate()
}
</script>

<template>
  <div class="ftree">
    <div class="toolbar">
      <button class="cbx" type="button" :class="allState" @click="toggleAll" aria-label="全选">
        <Check v-if="allState === 'checked'" />
        <Minus v-else-if="allState === 'indeterminate'" />
      </button>
      <span class="tlabel">{{ label || '全选文件' }}</span>
      <span class="count mono">{{ selectedCount }} / {{ totalLeaves }}</span>
    </div>

    <div class="roots">
      <FileTreeNode
        v-for="node in tree"
        :key="node.path"
        :node="node"
        :selected="selected"
        :level="0"
        @toggle="onChildToggle"
      />
    </div>
  </div>
</template>

<style scoped>
.ftree { display: flex; flex-direction: column; }
.toolbar {
  display: flex; align-items: center; gap: 9px;
  padding: 8px 10px;
  border-radius: var(--r-sm);
  background: var(--panel-2);
  border: 1px solid var(--line);
  margin-bottom: 5px;
}
.tlabel { font-size: var(--fs-sm); font-weight: var(--fw-semi); color: var(--ink-2); }
.count { margin-left: auto; font-size: var(--fs-xs); color: var(--ink-3); }
.cbx {
  display: grid; place-items: center; width: 18px; height: 18px;
  border: 1.5px solid var(--line-2); border-radius: var(--r-xs); background: var(--panel);
  transition: background var(--t-fast) var(--ease), border-color var(--t-fast) var(--ease);
}
.cbx.checked, .cbx.indeterminate { background: var(--brand); border-color: var(--brand); }
.cbx svg { width: 12px; height: 12px; color: var(--ink-on); }
.roots { display: flex; flex-direction: column; max-height: 360px; overflow-y: auto; }
</style>
