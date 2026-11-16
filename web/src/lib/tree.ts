/**
 * 方案树的纯函数编辑操作。
 *
 * 校对区的全部「修复模型漏收/重复」动作都落在这里。每个函数都返回新的
 * Section[] 而不就地修改，这样 Vue 的响应式更新和「撤销」都很容易，
 * 也不会出现半改状态被 PUT 出去。
 *
 * 这些函数只重排 node id、只改标题文本，**从不触碰原文** —— 原文节点始终
 * 只存在于 sourceDocument 里（逐字不变），方案只是对它们的引用。
 */

import type { Plan, Role, Section } from '../api/types'

/** 还原结构时需要的最少原文信息。 */
export interface SourceNodeRef {
  id: string
  kind: string
  level?: number
  order: number
}

/** 深拷贝一棵章节树。 */
export function cloneSections(sections: Section[]): Section[] {
  return sections.map((s) => ({
    ...s,
    body_node_ids: s.body_node_ids ? [...s.body_node_ids] : undefined,
    body_parts: s.body_parts?.map((bp) => ({ ...bp })),
    children: s.children ? cloneSections(s.children) : undefined,
  }))
}

/**
 * 深拷贝整个方案。
 *
 * `elements`、`preamble_node_ids` 与 `edits` 必须一并复制：`elements` 是
 * 「某个原文节点就是标题/成文日期/抄送」的声明，`preamble_node_ids` 是
 * 「这段原文是正文前言」，`edits` 则是「原文这里有已声明的改动」。丢掉任一字段，
 * 校验与成文结果都会跟着变 —— 丢掉 edits 尤其危险：改动不再被声明，那段文字
 * 就会被 VerifyVerbatim 判成「未声明的改写」而拒绝导出，而用户明明什么都没做。
 */
export function clonePlan(plan: Plan): Plan {
  return {
    ...plan,
    meta: { ...plan.meta },
    elements: plan.elements?.map((e) => ({ ...e })),
    preamble_node_ids: plan.preamble_node_ids ? [...plan.preamble_node_ids] : undefined,
    sections: cloneSections(plan.sections),
    edits: plan.edits?.map((e) => ({ ...e })),
    recompose: plan.recompose?.map((r) => ({
      ...r,
      parts: [...r.parts],
      roles: r.roles ? [...r.roles] : undefined,
    })),
    notes: plan.notes ? [...plan.notes] : undefined,
  }
}

/** 正文前言里是否已包含某节点。 */
export function preambleHas(plan: Plan | null | undefined, nodeId: string): boolean {
  return (plan?.preamble_node_ids ?? []).includes(nodeId)
}

/**
 * 把节点从正文前言中移除（不改动原文，只是不再由前言承载）。
 */
export function detachPreamble(plan: Plan, nodeId: string): Plan {
  return {
    ...plan,
    preamble_node_ids: (plan.preamble_node_ids ?? []).filter((id) => id !== nodeId),
  }
}

/**
 * 把节点加入正文前言。
 *
 * 同样先清掉它在别处的认领，因此「移入前言」天然不产生重复。
 * index 为 null 时按原文顺序插入（order 由调用方提供）。
 */
export function attachPreamble(
  plan: Plan,
  nodeId: string,
  order: Map<string, number>,
  index: number | null = null,
): Plan {
  const cleared = clearNodeClaims(plan, nodeId)
  const ids = [...(cleared.preamble_node_ids ?? [])]
  let at = ids.length
  if (index !== null) {
    at = Math.max(0, Math.min(index, ids.length))
  } else {
    const mine = order.get(nodeId)
    if (mine !== undefined) {
      at = ids.findIndex((id) => {
        const o = order.get(id)
        return o !== undefined && o > mine
      })
      if (at < 0) at = ids.length
    }
  }
  ids.splice(at, 0, nodeId)
  return { ...cleared, preamble_node_ids: ids }
}

/** 正文前言内上移/下移一个节点。 */
export function movePreamble(plan: Plan, nodeId: string, delta: -1 | 1): Plan {
  const ids = [...(plan.preamble_node_ids ?? [])]
  const i = ids.indexOf(nodeId)
  if (i < 0) return plan
  const j = i + delta
  if (j < 0 || j >= ids.length) return plan
  ;[ids[i], ids[j]] = [ids[j], ids[i]]
  return { ...plan, preamble_node_ids: ids }
}

/** 一个节点是否被绑定到某个公文要素槽位，若是返回该槽位。 */
export function elementRoleOf(plan: Plan | null | undefined, nodeId: string): string | null {
  return plan?.elements?.find((e) => e.node_id === nodeId)?.role ?? null
}

/**
 * 解除某个节点的要素绑定。
 *
 * 这是「漏收」的另一种成因：模型把一段正文错绑成了要素（或要素被绑错了节点），
 * 该节点不计入正文覆盖，用户需要先解开绑定，再把它作为正文收录进章节。
 */
export function detachElement(plan: Plan, nodeId: string): Plan {
  return { ...plan, elements: (plan.elements ?? []).filter((e) => e.node_id !== nodeId) }
}

/** 把节点绑定回某个公文要素槽位（先清掉它在别处的全部认领，避免重复）。 */
export function attachElement(plan: Plan, nodeId: string, role: Role): Plan {
  const cleared = clearNodeClaims(plan, nodeId)
  const elements = [...(cleared.elements ?? []).filter((e) => e.role !== role)]
  elements.push({ role, node_id: nodeId })
  return { ...cleared, elements }
}

/** 清空一个模型自拟的 meta 值（它没有对应原文，删掉不会丢内容）。 */
export function clearMeta(plan: Plan, field: keyof Plan['meta']): Plan {
  return { ...plan, meta: { ...plan.meta, [field]: '' } }
}

/**
 * 按原文把一个被并掉的章节恢复出来。
 *
 * 恢复的不是一个空壳：以该标题为起点，到下一个同级或更高级标题之前的正文
 * 都放回这一章（这些段落按原文本来就属于它）。范围内的子章节保持原样，
 * 只把章节树按原文顺序重新排一次。
 *
 * 这是「不同意模型把这一章并掉」的还原动作；它只搬运引用，不改一个字。
 */
export function restoreSectionFromHeading(
  plan: Plan,
  headingId: string,
  title: string,
  level: number,
  nodes: SourceNodeRef[],
  order: Map<string, number>,
): Plan {
  const headingOrder = order.get(headingId)
  if (headingOrder === undefined) return plan

  const headingLevel =
    level > 0 ? level : (nodes.find((n) => n.id === headingId)?.level ?? 1)

  // 下一个同级或更高级标题就是本章的终点。
  let limit = Number.POSITIVE_INFINITY
  for (const n of nodes) {
    if (n.kind !== 'heading') continue
    const o = order.get(n.id)
    if (o === undefined || o <= headingOrder) continue
    const lvl = Math.max(1, n.level ?? 1)
    if (lvl <= headingLevel && o < limit) limit = o
  }

  // 范围内的**子标题**也要排除：它们的正文属于子章节，不属于本层。
  // 只按层级判断，不按标题文字猜，恢复出来的树才和原文的分层一致。
  const subHeadingOrders: number[] = []
  for (const n of nodes) {
    if (n.kind !== 'heading') continue
    const o = order.get(n.id)
    if (o === undefined || o <= headingOrder || o >= limit) continue
    const lvl = Math.max(1, n.level ?? 1)
    if (lvl > headingLevel) subHeadingOrders.push(o)
  }
  const insideSubheading = (o: number): boolean => {
    let parent = -1
    for (const s of subHeadingOrders) {
      if (s < o && s > parent) parent = s
    }
    return parent >= 0
  }

  // 范围内、且没有被别的章节用作标题、也不属于子章节的正文，归回这一章。
  const headingIDs = new Set<string>()
  walkSections(plan.sections, (s) => {
    if (s.heading_node_id) headingIDs.add(s.heading_node_id)
  })
  // 已绑定成公文要素、或已被重组占用的节点不能当正文抢回来：前者会从
  // 版头/版记消失，后者会让重组分片找不到声明。
  const keepOut = new Set<string>()
  for (const e of plan.elements ?? []) keepOut.add(e.node_id)
  for (const r of plan.recompose ?? []) keepOut.add(r.node_id)
  const body: string[] = []
  for (const n of nodes) {
    if (n.id === headingId) continue
    const o = order.get(n.id)
    if (o === undefined || o <= headingOrder || o >= limit) continue
    if (n.kind === 'page_break' || n.kind === 'heading') continue
    if (headingIDs.has(n.id)) continue
    if (keepOut.has(n.id)) continue
    if (insideSubheading(o)) continue
    body.push(n.id)
  }

  let next = clearNodeClaims(plan, headingId)
  for (const id of body) next = clearNodeClaims(next, id)

  const section: Section = {
    id: nextSectionID(next.sections),
    title,
    level: headingLevel,
    heading_node_id: headingId,
    body_node_ids: body,
  }
  // 正文可能全部落在子章节里，因此 body 为空不代表恢复失败：章节本身与它的
  // 标题仍然是从原文恢复出来的，子章节会保留各自的正文。
  const sections = insertSectionInSourceOrder(next.sections, section, order)
  return { ...next, sections }
}

/**
 * 把新章节插到树里，并尽量放回它原本的父章节。
 *
 * 父章节 = 位于它之前、层级比它浅、且离它最近的那一章。找不到就放在根级。
 * 只按层级选父，不按标题猜，避免把一章挂到内容毫不相干的章节下面。
 */
function insertSectionInSourceOrder(
  sections: Section[],
  section: Section,
  order: Map<string, number>,
): Section[] {
  const anchor = sectionAnchorOrder(section, order)
  let parent: Section | null = null
  let parentAnchor = Number.NEGATIVE_INFINITY
  walkSections(sections, (s) => {
    if (s.level >= section.level) return
    const a = sectionAnchorOrder(s, order)
    if (a < anchor && a > parentAnchor) {
      parent = s
      parentAnchor = a
    }
  })

  if (!parent) {
    return sortSectionsBySourceOrder([...sections, section], order)
  }
  const parentID = (parent as Section).id
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === parentID) {
        const kids = sortSectionsBySourceOrder([...(s.children ?? []), section], order)
        return { ...s, children: kids }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return sortSectionsBySourceOrder(rec(sections), order)
}

/** 深度优先前序遍历所有章节（含嵌套），父在子前。 */
export function walkSections(
  sections: Section[],
  fn: (s: Section, parent: Section | null, depth: number) => void,
  parent: Section | null = null,
  depth = 0,
): void {
  for (const s of sections) {
    fn(s, parent, depth)
    if (s.children?.length) walkSections(s.children, fn, s, depth + 1)
  }
}

/** 展平为带深度的列表，供章节选择下拉框使用。 */
export interface FlatSection {
  section: Section
  depth: number
  /** 从根到该节点的 path 文本，如「一、总体要求 / （一）目标任务」。 */
  label: string
}

export function flattenSections(sections: Section[]): FlatSection[] {
  const out: FlatSection[] = []
  const rec = (list: Section[], depth: number, prefix: string): void => {
    for (const s of list) {
      const own = s.title?.trim() || s.id || '（未命名章节）'
      const label = prefix ? `${prefix} / ${own}` : own
      out.push({ section: s, depth, label })
      if (s.children?.length) rec(s.children, depth + 1, label)
    }
  }
  rec(sections, 0, '')
  return out
}

/** 按 id 找章节。 */
export function findSection(sections: Section[], id: string): Section | null {
  let found: Section | null = null
  walkSections(sections, (s) => {
    if (s.id === id) found = s
  })
  return found
}

/** 按标题找章节。原文标题与方案标题只在空白上有出入时也视为同一章。 */
export function findSectionByTitle(sections: Section[], title: string): Section | null {
  const want = titleKey(title)
  if (want === '') return null
  let found: Section | null = null
  walkSections(sections, (s) => {
    if (found === null && titleKey(s.title) === want) found = s
  })
  return found
}

/** 标题比较键：忽略首尾与中间空白，避免「一、总体 要求」被当成另一章。 */
export function titleKey(title: string): string {
  return (title ?? '').replace(/\s+/g, '')
}

/** 某个节点当前被哪些章节引用（正文位置）。 */
export function sectionsContainingNode(sections: Section[], nodeId: string): Section[] {
  const out: Section[] = []
  walkSections(sections, (s) => {
    if (s.body_node_ids?.includes(nodeId)) out.push(s)
  })
  return out
}

/**
 * 章节在原文里的起点。
 *
 * 优先用标题节点的原文位置；标题由模型自拟时，退到本节正文的第一段，
 * 再退到子章节。这个值只用于「按原文顺序放回去」，不改动任何文字。
 */
export function sectionAnchorOrder(section: Section, order: Map<string, number>): number {
  let min = Number.POSITIVE_INFINITY
  const bump = (id: string | undefined): void => {
    if (!id) return
    const o = order.get(id)
    if (o !== undefined && o < min) min = o
  }
  bump(section.heading_node_id)
  for (const id of section.body_node_ids ?? []) bump(id)
  for (const bp of section.body_parts ?? []) bump(bp.node_id)
  if (min === Number.POSITIVE_INFINITY) {
    for (const child of section.children ?? []) {
      const o = sectionAnchorOrder(child, order)
      if (o < min) min = o
    }
  }
  return min
}

/**
 * 按原文顺序找出某段应在的章节。
 *
 * 基线（baseline.Build）的规则是「前面最近的那个标题」。这里用同样的规则：
 * 在所有起点早于该节点的章节中，取起点最晚的一章。找不到时返回 null，
 * 表示它在第一个标题之前，应回到正文前言。
 */
export function sectionForSourceOrder(
  plan: Plan,
  nodeId: string,
  order: Map<string, number>,
): Section | null {
  const mine = order.get(nodeId)
  if (mine === undefined) return null
  let best: Section | null = null
  let bestOrder = Number.NEGATIVE_INFINITY
  walkSections(plan.sections, (s) => {
    const anchor = sectionAnchorOrder(s, order)
    if (anchor < mine && anchor > bestOrder) {
      best = s
      bestOrder = anchor
    }
  })
  return best
}

/** 递归按原文顺序重排每一层章节；起点相同的保持原有相对次序。 */
export function sortSectionsBySourceOrder(sections: Section[], order: Map<string, number>): Section[] {
  const rec = (list: Section[]): Section[] =>
    list
      .map((s, index) => ({
        section: { ...s, children: s.children ? rec(s.children) : undefined },
        index,
        anchor: sectionAnchorOrder(s, order),
      }))
      .sort((a, b) => {
        if (a.anchor === b.anchor) return a.index - b.index
        return a.anchor - b.anchor
      })
      .map((x) => x.section)
  return rec(sections)
}

/** 把某一章节的正文按原文顺序重排（只改引用顺序，不改文字）。 */
export function sortBodyBySourceOrder(
  sections: Section[],
  sectionId: string,
  order: Map<string, number>,
): Section[] {
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      const next: Section = { ...s, children: s.children ? rec(s.children) : undefined }
      if (s.id !== sectionId) return next
      const body = [...(s.body_node_ids ?? [])]
      body.sort((a, b) => (order.get(a) ?? Number.MAX_SAFE_INTEGER) - (order.get(b) ?? Number.MAX_SAFE_INTEGER))
      return { ...next, body_node_ids: body, preserve_order: false }
    })
  return rec(sections)
}

/**
 * 拆掉一个由模型自拟标题的章节，把它承载的内容并回最近的可承接位置。
 *
 * 这是「不同意新增章节」的还原动作：标题是模型写的，删掉不丢原文；正文与
 * 子章节必须留下，否则会出现「内容突然不见了」。合并目标依次是父章节、前一个
 * 同级章节；两者都没有时，正文回到正文前言（它本来就排在这些章节之前）。
 *
 * 返回 null 表示没有安全的位置可放，调用方应保持原样并说明原因。
 */
export function dissolveSection(
  plan: Plan,
  sectionId: string,
  order: Map<string, number>,
): Plan | null {
  const found = locateSection(plan.sections, sectionId, null)
  if (!found) return null
  const victim = found.list[found.index]
  if (victim.heading_node_id) return null

  // 先把章节本身拿掉（子章节提升到原位置），再把它的正文并入相邻章节。
  //
  // 不按「原文归属」重算：模型新增章节最常见的场景就是原文本来没有标题，
  // 这时按原文归属算回去只会把它们放进另一个同样新增的章节里，用户看到的是
  // 「点了不同意，段落却跑到别处」。并入相邻章节是唯一可预期的结果。
  const sections = removeSectionPromoteChildren(plan.sections, sectionId)
  let nextPlan: Plan = { ...plan, sections }

  const target = neighbourSection(found)?.id ?? ''
  const parts = victim.body_parts ?? []
  if (target === '' && ((victim.body_node_ids ?? []).length > 0 || parts.length > 0)) {
    // 没有相邻章节可并：只能回到正文前言，正文才不至于消失。
    for (const id of victim.body_node_ids ?? []) nextPlan = attachPreamble(nextPlan, id, order)
    for (const bp of parts) {
      const at = sectionForSourceOrder(nextPlan, bp.node_id, order)
      if (!at) return null
      nextPlan = { ...nextPlan, sections: appendBodyParts(nextPlan.sections, at.id, [bp]) }
    }
    return nextPlan
  }
  for (const id of victim.body_node_ids ?? []) {
    nextPlan = placeBodyNodeInSourceOrder(nextPlan, target, id, order)
  }
  if (parts.length > 0) {
    nextPlan = { ...nextPlan, sections: appendBodyParts(nextPlan.sections, target, parts) }
  }

  return nextPlan
}

/** 章节的前一个同级/父级、后一个同级章节，作为合并目标。 */
function neighbourSection(found: {
  list: Section[]
  index: number
  parent: Section | null
}): Section | null {
  return found.parent ?? found.list[found.index + 1] ?? found.list[found.index - 1] ?? null
}

/**
 * 不同意「新增章节」并把它还原成正文。
 *
 * 与 dissolveSection 的区别：这里的章节标题来自原文某个段落（模型把一段普通
 * 文字当成了标题）。只删章节会把那段文字一起弄丢，所以标题段落要作为正文放回
 * 相邻章节；原文那段文字一个字都不改。
 */
export function revertAddedSection(
  plan: Plan,
  sectionId: string,
  order: Map<string, number>,
): Plan | null {
  const found = locateSection(plan.sections, sectionId, null)
  if (!found) return null
  const victim = found.list[found.index]
  const ids = [
    ...(victim.heading_node_id ? [victim.heading_node_id] : []),
    ...(victim.body_node_ids ?? []),
  ]
  const parts = victim.body_parts ?? []
  const target = neighbourSection(found)

  let next: Plan = { ...plan, sections: removeSectionPromoteChildren(plan.sections, sectionId) }
  const hasTarget = target !== null && locateSection(next.sections, target.id, null) !== null
  if (!hasTarget && parts.length > 0) return null

  for (const id of ids) {
    next = hasTarget && target
      ? placeBodyNodeInSourceOrder(next, target.id, id, order)
      : attachPreamble(next, id, order)
  }
  if (hasTarget && target && parts.length > 0) {
    next = { ...next, sections: appendBodyParts(next.sections, target.id, parts) }
  }
  return next
}

/** 找到章节所在的列表、下标与父章节。 */
function locateSection(
  list: Section[],
  id: string,
  parent: Section | null,
): { list: Section[]; index: number; parent: Section | null } | null {
  for (let i = 0; i < list.length; i++) {
    if (list[i].id === id) return { list, index: i, parent }
    const child = locateSection(list[i].children ?? [], id, list[i])
    if (child) return child
  }
  return null
}

/** 移除一个章节，把它的子章节提升到它原来的位置。 */
function removeSectionPromoteChildren(list: Section[], id: string): Section[] {
  const out: Section[] = []
  for (const s of list) {
    if (s.id === id) {
      out.push(...(s.children ?? []))
      continue
    }
    out.push({ ...s, children: s.children ? removeSectionPromoteChildren(s.children, id) : undefined })
  }
  return out
}

/** 从树里摘掉一个章节（连同它的子树），返回剩下的树与被摘掉的章节。 */
function detachSection(
  list: Section[],
  id: string,
): { sections: Section[]; victim: Section | null } {
  let victim: Section | null = null
  const rec = (items: Section[]): Section[] => {
    const out: Section[] = []
    for (const s of items) {
      if (s.id === id) {
        victim = s
        continue
      }
      out.push({ ...s, children: s.children ? rec(s.children) : undefined })
    }
    return out
  }
  const sections = rec(list)
  return { sections, victim }
}

/**
 * 把一章挂回原文里的父章节（或顶层）。
 *
 * 「不同意换上级」只改它在树里的位置，不改标题、不改层级、也不动正文。
 * 目标父章节按它的原文标题节点识别 —— 这是两份方案之间唯一稳定的同一性：
 * 基线的 id 是 b1…，模型的是 s1…。
 *
 * 返回 null 表示不能安全移动（目标父章节不在方案里，或目标是自己的子孙，
 * 移过去会形成环），调用方应保留原样并说明原因。
 */
export function reparentSection(
  sections: Section[],
  sectionId: string,
  parentHeadingId: string,
  order: Map<string, number>,
): Section[] | null {
  const { sections: rest, victim } = detachSection(sections, sectionId)
  if (!victim) return null

  if (parentHeadingId === '') {
    return sortSectionsBySourceOrder([...rest, { ...victim, children: victim.children }], order)
  }

  let parent: Section | null = null
  let insideVictim = false
  walkSections(rest, (s) => {
    if (s.heading_node_id === parentHeadingId) parent = s
  })
  walkSections(victim.children ?? [], (s) => {
    if (s.heading_node_id === parentHeadingId) insideVictim = true
  })
  if (insideVictim || !parent) return null

  const parentId = (parent as Section).id
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === parentId) {
        const kids = sortSectionsBySourceOrder([...(s.children ?? []), victim as Section], order)
        return { ...s, children: kids }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return sortSectionsBySourceOrder(rec(rest), order)
}

/** 把重组分片追加到某章节（去重）。 */
function appendBodyParts(sections: Section[], sectionId: string, parts: NonNullable<Section['body_parts']>): Section[] {
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      const next: Section = { ...s, children: s.children ? rec(s.children) : undefined }
      if (s.id !== sectionId) return next
      const merged = [...(s.body_parts ?? [])]
      for (const bp of parts) {
        if (!merged.some((x) => x.node_id === bp.node_id && x.part === bp.part)) merged.push({ ...bp })
      }
      return { ...next, body_parts: merged }
    })
  return rec(sections)
}

// ---------------------------------------------------------------- claim sites

/**
 * 一个原文节点被方案「认领」的位置。
 *
 * docmodel.VerifyPlan 在一个节点被认领多于一次时报 duplicate_content（error），
 * 而认领有**四种**互不相同的位置：
 *
 *   - `element`：Plan.Elements 把它绑成标题/成文日期/抄送等公文要素；
 *   - `preamble`：Plan.PreambleNodeIDs 把它作为正文前言；
 *   - `heading`：某章节的 heading_node_id 指向它；
 *   - `body`：某章节的 body_node_ids 收录它。
 *
 * 只盯着 body 去重是不够的 —— 模型把同一段既绑成要素、又写进前言、又收进
 * 章节时，重复依然存在但用户看不到、也修不掉。所以这里把四种位置统一成一张表。
 */
export type ClaimKind = 'element' | 'preamble' | 'heading' | 'body'

export interface ClaimSite {
  kind: ClaimKind
  /** element 位置为 null。 */
  sectionId: string | null
  /** element 位置为要素 role；preamble/heading/body 为 null。 */
  role: string | null
  /**
   * body 位置在 body_node_ids 里的下标；element/preamble/heading 为 null。
   *
   * 必须有它：VerifyPlan 对 body_node_ids 里**每一次出现**都记一次认领，
   * 所以同一章节把同一段落写两遍也是重复。只用 includes() 判断存在性会漏掉
   * 这种「同节内重复」，用户就会遇到「明明点了合并却仍然报重复」。
   */
  index: number | null
  /** 界面上的中文描述，直接可用于按钮文案。 */
  label: string
}

/**
 * 列出某个节点被认领的全部位置，**同一位置重复出现的每一次都各算一处**。
 *
 * 顺序刻意照 VerifyPlan 认领的先后：element → preamble → heading → body。
 * 因此 claims[0] 就是「第一处」，与用户点「只保留第一处」的预期一致。
 *
 * 共四种认领位置（漏掉任何一种，用户都会遇到「点了合并却修不好」）：
 *   1. `element`  —— Plan.Elements 绑成标题/成文日期/抄送等公文要素；
 *   2. `preamble` —— Plan.PreambleNodeIDs 正文前言；
 *   3. `heading`  —— 某章节的 heading_node_id；
 *   4. `body`     —— 某章节的 body_node_ids（每一次出现各算一处）。
 */
export function claimSites(plan: Plan, nodeId: string): ClaimSite[] {
  const out: ClaimSite[] = []

  for (const e of plan.elements ?? []) {
    if (e.node_id === nodeId) {
      out.push({
        kind: 'element',
        sectionId: null,
        role: e.role,
        index: null,
        label: `要素「${roleLabel(e.role)}」`,
      })
    }
  }

  ;(plan.preamble_node_ids ?? []).forEach((id, i) => {
    if (id === nodeId) {
      out.push({
        kind: 'preamble',
        sectionId: null,
        role: null,
        index: i,
        label: `正文前言第 ${i + 1} 段`,
      })
    }
  })

  walkSections(plan.sections ?? [], (s) => {
    if (s.heading_node_id === nodeId) {
      out.push({
        kind: 'heading',
        sectionId: s.id,
        role: null,
        index: null,
        label: `章节「${s.title || s.id}」的标题`,
      })
    }
  })

  walkSections(plan.sections ?? [], (s) => {
    ;(s.body_node_ids ?? []).forEach((id, i) => {
      if (id === nodeId) {
        out.push({
          kind: 'body',
          sectionId: s.id,
          role: null,
          index: i,
          label: `章节「${s.title || s.id}」的正文第 ${i + 1} 处`,
        })
      }
    })
  })

  return out
}

/** 要素 role 的中文名（与 api/types.ts 的 ROLE_LABEL 保持一致，此处内联以免循环依赖）。 */
function roleLabel(role: string): string {
  const map: Record<string, string> = {
    copy_number: '份号',
    security: '密级',
    urgency: '紧急程度',
    masthead: '发文机关标志',
    doc_number: '发文字号',
    signatory: '签发人',
    title: '标题',
    recipient: '主送机关',
    attachment_note: '附件说明',
    signature: '发文机关署名',
    date: '成文日期',
    note: '附注',
    cc: '抄送机关',
    printer: '印发机关',
    body: '正文',
  }
  return map[role] ?? role
}

/** 去掉指定位置的一处认领（只删这一处，不影响同节点在别处的认领）。 */
export function removeClaimSite(plan: Plan, nodeId: string, site: ClaimSite): Plan {
  switch (site.kind) {
    case 'element':
      // 要素槽位一对一，直接删掉该节点的绑定。
      return { ...plan, elements: (plan.elements ?? []).filter((e) => e.node_id !== nodeId) }
    case 'preamble': {
      const ids = [...(plan.preamble_node_ids ?? [])]
      if (site.index !== null && ids[site.index] === nodeId) ids.splice(site.index, 1)
      else return { ...plan, preamble_node_ids: ids.filter((id) => id !== nodeId) }
      return { ...plan, preamble_node_ids: ids }
    }
    case 'heading':
      return {
        ...plan,
        sections: mapSections(plan.sections, (s) => {
          if (s.id !== site.sectionId || s.heading_node_id !== nodeId) return s
          const next: Section = { ...s }
          delete next.heading_node_id
          return next
        }),
      }
    case 'body':
      return {
        ...plan,
        sections: mapSections(plan.sections, (s) => {
          if (s.id !== site.sectionId) return s
          return { ...s, body_node_ids: (s.body_node_ids ?? []).filter((_, i) => i !== site.index) }
        }),
      }
  }
}

/**
 * 把某个节点的多处认领合并为**一处**：保留 `keep`，移除其余全部位置。
 *
 * 实现是「先全部清掉、再放回保留的那一处」，而不是逐处删除。原因是同一章节内
 * 可能有多个下标指向同一节点，逐个删会让下标移位、删错位置。
 */
export function consolidateNode(plan: Plan, nodeId: string, keep: ClaimSite): Plan {
  // 1) 记录保留处所需的上下文。
  const keptSection = keep.sectionId ? findSection(plan.sections, keep.sectionId) : null
  const keptHeadingTitle = keptSection?.title ?? ''

  // 2) 清掉该节点的所有认领。
  let next: Plan = {
    ...plan,
    elements: (plan.elements ?? []).filter((e) => e.node_id !== nodeId),
    preamble_node_ids: (plan.preamble_node_ids ?? []).filter((id) => id !== nodeId),
  }
  next = {
    ...next,
    sections: mapSections(next.sections, (s) => {
      const out: Section = { ...s }
      if (out.heading_node_id === nodeId) delete out.heading_node_id
      if (out.body_node_ids?.length) {
        out.body_node_ids = out.body_node_ids.filter((id) => id !== nodeId)
      }
      return out
    }),
  }

  // 3) 把保留的那一处放回去。
  switch (keep.kind) {
    case 'element':
      return { ...next, elements: [...(next.elements ?? []), { role: keep.role as Role, node_id: nodeId }] }
    case 'preamble': {
      const ids = [...(next.preamble_node_ids ?? [])]
      const at = Math.max(0, Math.min(keep.index ?? ids.length, ids.length))
      ids.splice(at, 0, nodeId)
      return { ...next, preamble_node_ids: ids }
    }
    case 'heading':
      return {
        ...next,
        sections: mapSections(next.sections, (s) =>
          s.id === keep.sectionId ? { ...s, heading_node_id: nodeId, title: keptHeadingTitle } : s,
        ),
      }
    case 'body': {
      const at = keep.index ?? 0
      return { ...next, sections: insertBodyNode(next.sections, keep.sectionId ?? '', nodeId, at) }
    }
  }
}

/**
 * 批量修复：对每个被多处认领的节点保留其第一处。
 *
 * 与界面「合并 N 段重复」按钮对应。返回的新方案里不再有重复认领。
 */
export function dedupeAllNodes(plan: Plan, nodeIds: string[]): Plan {
  let next = plan
  for (const id of nodeIds) {
    const sites = claimSites(next, id)
    if (sites.length > 1) next = consolidateNode(next, id, sites[0])
  }
  return next
}

/** 对所有章节做一次不就地修改的映射。 */
function mapSections(list: Section[], fn: (s: Section) => Section): Section[] {
  return list.map((s) => {
    const mapped = fn({ ...s, children: s.children ? mapSections(s.children, fn) : undefined })
    return mapped
  })
}

/** 某个节点是否被用作某章节的标题节点。 */
export function sectionUsingHeading(sections: Section[], nodeId: string): Section | null {
  let found: Section | null = null
  walkSections(sections, (s) => {
    if (s.heading_node_id === nodeId) found = s
  })
  return found
}

/**
 * 把节点从**所有**章节的正文中移除。这是修复「重复收录」的核心动作：
 * 模型把同一段落放进两个章节时，覆盖数会小于正文数且报 duplicate_content，
 * 用户点一次「移除重复」就能让它只剩一处。
 */
export function removeNodeEverywhere(sections: Section[], nodeId: string): Section[] {
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      const next: Section = { ...s, children: s.children ? rec(s.children) : undefined }
      if (next.body_node_ids?.includes(nodeId)) {
        next.body_node_ids = next.body_node_ids.filter((id) => id !== nodeId)
      }
      return next
    })
  return rec(sections)
}

/**
 * 清掉一个节点在方案里的**全部**认领（要素绑定、正文前言、章节标题、章节正文）。
 *
 * 这是「移动」类操作的前置步骤：先清干净再放到新位置，移动就天然不会产生重复。
 * 各字段必须一起清 —— 只清 body 时，把一段从章节移入前言仍会留下重复。
 */
export function clearNodeClaims(plan: Plan, nodeId: string): Plan {
  const sections = mapSections(plan.sections, (s) => {
    const next: Section = { ...s }
    if (next.heading_node_id === nodeId) delete next.heading_node_id
    if (next.body_node_ids?.length) {
      next.body_node_ids = next.body_node_ids.filter((id) => id !== nodeId)
    }
    return next
  })
  return {
    ...plan,
    elements: (plan.elements ?? []).filter((e) => e.node_id !== nodeId),
    preamble_node_ids: (plan.preamble_node_ids ?? []).filter((id) => id !== nodeId),
    sections,
  }
}

/**
 * 把节点放入某章节正文，位置按原文顺序（先清掉它在别处的全部认领）。
 * 对应界面上的「收录」「移入其他章节」。
 */
export function placeBodyNodeInSourceOrder(
  plan: Plan,
  sectionId: string,
  nodeId: string,
  order: Map<string, number>,
): Plan {
  const cleared = clearNodeClaims(plan, nodeId)
  return { ...cleared, sections: insertBodyNodeInSourceOrder(cleared.sections, sectionId, nodeId, order) }
}

/**
 * 把正文节点插入到指定章节的指定下标。插入前先从其他章节移除，从而
 * 「移动」天然不会产生重复。
 */
export function insertBodyNode(
  sections: Section[],
  sectionId: string,
  nodeId: string,
  index: number,
): Section[] {
  const cleared = removeNodeEverywhere(sections, nodeId)
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === sectionId) {
        const ids = [...(s.body_node_ids ?? [])]
        const at = Math.max(0, Math.min(index, ids.length))
        ids.splice(at, 0, nodeId)
        return { ...s, body_node_ids: ids, children: s.children ? rec(s.children) : undefined }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return rec(cleared)
}

/**
 * 按原文顺序把正文节点插入章节。
 *
 * 「收录」按钮用它而不是简单追加到末尾：VerifyPlan 会对「节内顺序与原文不一致」
 * 报 reordered 警告，把一段本该在中间的原文追加到末尾会平白多出一条警告，
 * 而用户的本意只是「别丢了这段」。order 由调用方从原文节点提供。
 */
export function insertBodyNodeInSourceOrder(
  sections: Section[],
  sectionId: string,
  nodeId: string,
  order: Map<string, number>,
): Section[] {
  const cleared = removeNodeEverywhere(sections, nodeId)
  const mine = order.get(nodeId)
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === sectionId) {
        const ids = [...(s.body_node_ids ?? [])]
        let at = ids.length
        if (mine !== undefined) {
          at = ids.findIndex((id) => {
            const o = order.get(id)
            return o !== undefined && o > mine
          })
          if (at < 0) at = ids.length
        }
        ids.splice(at, 0, nodeId)
        return { ...s, body_node_ids: ids, children: s.children ? rec(s.children) : undefined }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return rec(cleared)
}

/** 章节内上移/下移一个正文节点；越界时原样返回。 */
export function moveBodyNode(
  sections: Section[],
  sectionId: string,
  nodeId: string,
  delta: -1 | 1,
): Section[] {
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === sectionId) {
        const ids = [...(s.body_node_ids ?? [])]
        const i = ids.indexOf(nodeId)
        if (i < 0) return s
        const j = i + delta
        if (j < 0 || j >= ids.length) return s
        ;[ids[i], ids[j]] = [ids[j], ids[i]]
        return { ...s, body_node_ids: ids, children: s.children ? rec(s.children) : undefined }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return rec(sections)
}

/** 从章节正文中移除一个节点（只移除该章节这一处）。 */
export function detachBodyNode(sections: Section[], sectionId: string, nodeId: string): Section[] {
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === sectionId) {
        return {
          ...s,
          body_node_ids: (s.body_node_ids ?? []).filter((id) => id !== nodeId),
          children: s.children ? rec(s.children) : undefined,
        }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return rec(sections)
}

/**
 * 修改章节标题。
 *
 * 注意一个真实的陷阱：若该章节的标题来自原文节点（heading_node_id 非空），
 * VerifyPlan 要求 Title 与原文逐字相同，改了就会报 heading_mismatch。
 * 界面据此提示用户「改标题 = 改原文标题，要么改回，要么解除与原文的关联」。
 */
export function setSectionTitle(sections: Section[], sectionId: string, title: string): Section[] {
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === sectionId) {
        // 标题被用户改动后，模型若曾声明「节内顺序经确认调整」，该声明不再可信。
        return { ...s, title, preserve_order: false }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return rec(sections)
}

/**
 * 修改章节层级；会让树不合法时返回 null。
 *
 * 层级不是孤立数字：它必须比父级深、比每个子级浅。模型把一个章节整体
 * 挪深/挪浅时，只有满足这两个约束才能还原，否则应当交给人手动调整。
 */
export function setSectionLevel(sections: Section[], sectionId: string, level: number): Section[] | null {
  const found = locateSection(sections, sectionId, null)
  if (!found) return null
  const victim = found.list[found.index]
  if (level < 1 || level > 4) return null
  if (found.parent && level <= found.parent.level) return null
  if ((victim.children ?? []).some((c) => c.level <= level)) return null

  const rec = (list: Section[]): Section[] =>
    list.map((s) =>
      s.id === sectionId
        ? { ...s, level }
        : { ...s, children: s.children ? rec(s.children) : undefined },
    )
  return rec(sections)
}

/** 解除章节标题与原文节点的关联，使其成为可自由编辑的自拟标题。 */
export function detachSectionHeading(sections: Section[], sectionId: string): Section[] {
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === sectionId) {
        const next: Section = { ...s, preserve_order: false }
        delete next.heading_node_id
        return next
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return rec(sections)
}

/** 把原文标题节点重新关联到章节，并把标题同步为原文文本。 */
export function attachSectionHeading(
  sections: Section[],
  sectionId: string,
  nodeId: string,
  nodeText: string,
): Section[] {
  const cleared = removeNodeEverywhere(sections, nodeId)
  const dropHeading = (list: Section[]): Section[] =>
    list.map((s) => {
      const next: Section = { ...s, children: s.children ? dropHeading(s.children) : undefined }
      if (next.heading_node_id === nodeId) delete next.heading_node_id
      return next
    })
  const rec = (list: Section[]): Section[] =>
    list.map((s) => {
      if (s.id === sectionId) {
        return { ...s, heading_node_id: nodeId, title: nodeText }
      }
      return { ...s, children: s.children ? rec(s.children) : undefined }
    })
  return rec(dropHeading(cleared))
}

/** 新建一个空章节，追加在根级末尾（用于把散落内容归入新标题）。 */
export function appendSection(sections: Section[], title: string): { sections: Section[]; id: string } {
  const id = nextSectionID(sections)
  const section: Section = { id, title, level: 1, body_node_ids: [] }
  return { sections: [...sections, section], id }
}

/** 生成一个不与现有章节冲突的 id。 */
export function nextSectionID(sections: Section[]): string {
  const used = new Set<string>()
  walkSections(sections, (s) => used.add(s.id))
  let n = used.size + 1
  let id = `s${String(n).padStart(4, '0')}`
  while (used.has(id)) {
    n++
    id = `s${String(n).padStart(4, '0')}`
  }
  return id
}

/** 统计方案树里的正文引用总数（含重复），供 UI 提示用。 */
export function countBodyRefs(sections: Section[]): number {
  let n = 0
  walkSections(sections, (s) => {
    n += s.body_node_ids?.length ?? 0
  })
  return n
}
