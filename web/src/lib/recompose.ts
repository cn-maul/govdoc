/**
 * 重组的「提案 / 已接受」状态转换。
 *
 * 模型返回的 recompose 是建议，不是用户已经接受的结果。若直接把它留在
 * plan.recompose 里，预览会立刻显示模型改写后的段落，校对区再点「不同意」
 * 就已经没有意义了。
 *
 * 因此模型结果先被拆成两半：
 *   - 一份可提交的骨架方案（原文段落按普通 body_node_ids 放回原处）；
 *   - 一组待校对的重组项，保存原文节点与它在各章节中的精确落点。
 *
 * 用户同意时才把该节点的 recompose 和 body_parts 一起写回方案。两者必须
 * 原子地成对出现：只有 body_parts 而没有 recompose 会被服务端判为
 * undeclared_body_part，只有 recompose 而没有 body_parts 则会漏收原文。
 */

import type { Plan, Recompose, Section } from '../api/types'

/** 一个重组分片在章节树里的落点。 */
export interface RecomposePlacement {
  section_id: string
  part: number
}

/** 一条待校对的重组。 */
export interface PendingRecompose {
  recompose: Recompose
  /** 分片原定放入哪些章节；同意时按此精确恢复。 */
  placements: RecomposePlacement[]
}

/**
 * 把模型方案拆成「不含重组、可先预览原文」的骨架与待审重组。
 *
 * 对每个被重组的源节点：
 *   1. 从章节树里移除它的全部 body_parts；
 *   2. 把源节点整体放回第一个曾引用它的章节，保证骨架仍然覆盖原文；
 *   3. 记录它原本每个 part 的落点，供同意时恢复。
 */
export function splitRecompose(plan: Plan): {
  plan: Plan
  pending: PendingRecompose[]
} {
  const pending: PendingRecompose[] = []
  for (const rec of plan.recompose ?? []) {
    const placements: RecomposePlacement[] = []
    walk(plan.sections, (section) => {
      for (const bp of section.body_parts ?? []) {
        if (bp.node_id === rec.node_id) {
          placements.push({ section_id: section.id, part: bp.part })
        }
      }
    })
    // A recomposition with only recipient/preamble parts has no body_parts
    // entry, but it still must be reviewed and accepted as a unit. Dropping it
    // here would leave the original node unclaimed once plan.recompose is
    // cleared for the preview skeleton.
    pending.push({ recompose: cloneRecompose(rec), placements })
  }

  const pendingIDs = new Set(pending.map((p) => p.recompose.node_id))
  if (pendingIDs.size === 0) {
    return { plan: { ...plan, recompose: [] }, pending }
  }

  let sections = stripParts(plan.sections, pendingIDs)
  const placed = placedNodeIDs(sections)
  let preamble = [...(plan.preamble_node_ids ?? [])]
  for (const item of pending) {
    const id = item.recompose.node_id
    if (placed.has(id)) continue
    if (item.placements.length > 0) {
      // The node is normally re-inserted by stripParts at its first part's
      // section. Keep this guard for a plan whose placement section vanished.
      sections = insertWholeNode(sections, item.placements[0].section_id, id)
      placed.add(id)
      continue
    }
    // A recomposition made only of recipient/preamble parts has no chapter
    // placement at all. Showing it in the first chapter would claim it belongs
    // under a heading that the model never chose; the unnumbered preamble is
    // the faithful place for the original paragraph.
    if (!preamble.includes(id)) {
      preamble.push(id)
      placed.add(id)
    }
  }

  return {
    plan: {
      ...plan,
      sections,
      preamble_node_ids: preamble,
      recompose: [],
    },
    pending,
  }
}

/**
 * 把一条已同意的重组写回方案。
 *
 * 源节点在骨架里是以普通正文出现的，写回前必须先把它从所有 body_node_ids
 * 中移除；否则同一段既整体出现一次、又按分片出现一次，校验会判重复。
 */
export function applyRecompose(plan: Plan, pending: PendingRecompose): Plan {
  const nodeID = pending.recompose.node_id
  const sections = removeWholeNode(plan.sections, nodeID)
  const recompose = [
    ...(plan.recompose ?? []).filter((r) => r.node_id !== nodeID),
    cloneRecompose(pending.recompose),
  ]

  for (const placement of pending.placements) {
    const target = findSection(sections, placement.section_id)
    if (!target) continue
    const parts = [...(target.body_parts ?? [])]
    if (parts.some((bp) => bp.node_id === nodeID && bp.part === placement.part)) continue
    parts.push({ node_id: nodeID, part: placement.part })
    target.body_parts = parts
  }

  return {
    ...plan,
    sections,
    recompose,
    preamble_node_ids: (plan.preamble_node_ids ?? []).filter((id) => id !== nodeID),
  }
}

function findSection(list: Section[], id: string): Section | null {
  for (const section of list) {
    if (section.id === id) return section
    if (section.children?.length) {
      const found = findSection(section.children, id)
      if (found) return found
    }
  }
  return null
}

/** 深拷贝一条重组声明。 */
export function cloneRecompose(rec: Recompose): Recompose {
  return {
    ...rec,
    parts: [...rec.parts],
    roles: rec.roles ? [...rec.roles] : undefined,
  }
}

/** 当前章节树里已经以整段形式收录的节点。 */
function placedNodeIDs(list: Section[]): Set<string> {
  const out = new Set<string>()
  walk(list, (section) => {
    for (const id of section.body_node_ids ?? []) out.add(id)
  })
  return out
}

/** 把整段原文追加到指定章节，作为接受重组前的预览骨架。 */
function insertWholeNode(list: Section[], sectionID: string, nodeID: string): Section[] {
  return list.map((section) => {
    const next: Section = { ...section }
    if (section.id === sectionID) {
      const body = [...(section.body_node_ids ?? [])]
      if (!body.includes(nodeID)) body.push(nodeID)
      next.body_node_ids = body
    }
    if (section.children?.length) next.children = insertWholeNode(section.children, sectionID, nodeID)
    return next
  })
}

function walk(list: Section[], fn: (s: Section) => void): void {
  for (const section of list) {
    fn(section)
    if (section.children?.length) walk(section.children, fn)
  }
}

function stripParts(list: Section[], nodeIDs: Set<string>): Section[] {
  const placed = new Set<string>()
  return list.map((section) => {
    const removed = (section.body_parts ?? []).filter((bp) => nodeIDs.has(bp.node_id))
    const parts = (section.body_parts ?? []).filter((bp) => !nodeIDs.has(bp.node_id))
    const body = [...(section.body_node_ids ?? [])]

    for (const bp of removed) {
      if (placed.has(bp.node_id)) continue
      placed.add(bp.node_id)
      body.push(bp.node_id)
    }

    const next: Section = { ...section }
    if (parts.length > 0) next.body_parts = parts
    else delete next.body_parts
    if (body.length > 0) next.body_node_ids = body
    else delete next.body_node_ids
    if (section.children?.length) next.children = stripParts(section.children, nodeIDs)
    return next
  })
}

function removeWholeNode(list: Section[], nodeID: string): Section[] {
  return list.map((section) => {
    const next: Section = { ...section }
    const body = (section.body_node_ids ?? []).filter((id) => id !== nodeID)
    const parts = (section.body_parts ?? []).filter((bp) => bp.node_id !== nodeID)
    if (body.length > 0) next.body_node_ids = body
    else delete next.body_node_ids
    if (parts.length > 0) next.body_parts = parts
    else delete next.body_parts
    if (section.children?.length) next.children = removeWholeNode(section.children, nodeID)
    return next
  })
}

/** 统计重组后的段落数，供校对卡片显示 1 段 → N 段。 */
export function recomposePartCount(rec: Recompose): number {
  return rec.parts.length
}
