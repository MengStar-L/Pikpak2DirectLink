// Builds a selectable tree out of a flat list of DownloadItems (which carry a
// slash-delimited `path`). Real items (with an id) are selectable; purely
// structural folders derived from path segments only group their descendants.
import type { DownloadItem } from './types'

export type TreeNode = {
  name: string
  path: string
  item?: DownloadItem
  children: TreeNode[]
}

export function buildTree(items: DownloadItem[]): TreeNode[] {
  const root: TreeNode = { name: '', path: '', children: [] }

  for (const it of items) {
    const raw = (it.path || it.name).replace(/^\//, '')
    const segs = raw.split('/').filter((s) => s.length > 0)
    if (segs.length === 0) {
      attachToNode(root, it.name, it.name, it)
      continue
    }
    let cur = root
    let acc = ''
    for (let i = 0; i < segs.length; i++) {
      const seg = segs[i]
      acc = acc ? `${acc}/${seg}` : seg
      const last = i === segs.length - 1
      let child = cur.children.find((c) => c.name === seg)
      if (!child) {
        child = { name: seg, path: acc, children: [] }
        cur.children.push(child)
      }
      if (last) child.item = it
      cur = child
    }
  }

  sortNodes(root.children)
  return root.children
}

function attachToNode(parent: TreeNode, name: string, path: string, item: DownloadItem) {
  let child = parent.children.find((c) => c.name === name)
  if (!child) {
    child = { name, path, children: [] }
    parent.children.push(child)
  }
  child.item = item
}

function sortNodes(nodes: TreeNode[]) {
  nodes.sort((a, b) => {
    const af = a.children.length > 0 ? 0 : 1
    const bf = b.children.length > 0 ? 0 : 1
    if (af !== bf) return af - bf
    return a.name.localeCompare(b.name, 'zh')
  })
  nodes.forEach((n) => sortNodes(n.children))
}

/** Every selectable item id within a subtree. */
export function selectableIds(node: TreeNode): string[] {
  const out: string[] = []
  const walk = (n: TreeNode) => {
    if (n.item && n.item.id) out.push(n.item.id)
    n.children.forEach(walk)
  }
  walk(node)
  return out
}

export type CheckState = 'unchecked' | 'checked' | 'indeterminate'

export function checkState(node: TreeNode, selected: Set<string>): CheckState {
  const ids = selectableIds(node)
  if (ids.length === 0) return 'unchecked'
  let hit = 0
  for (const id of ids) if (selected.has(id)) hit++
  if (hit === 0) return 'unchecked'
  if (hit === ids.length) return 'checked'
  return 'indeterminate'
}

/** Toggle a node: if fully checked (or indeterminate) clear its subtree,
 * otherwise select every selectable descendant. Mutates the set in place. */
export function toggleNode(node: TreeNode, selected: Set<string>) {
  const ids = selectableIds(node)
  const state = checkState(node, selected)
  if (state === 'checked' || state === 'indeterminate') {
    ids.forEach((id) => selected.delete(id))
  } else {
    ids.forEach((id) => selected.add(id))
  }
}

export function countLeaves(nodes: TreeNode[]): number {
  let n = 0
  const walk = (list: TreeNode[]) => {
    for (const node of list) {
      if (node.item) n++
      if (node.children.length) walk(node.children)
    }
  }
  walk(nodes)
  return n
}
