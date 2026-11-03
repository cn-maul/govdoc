/**
 * 改动的纯函数编辑操作。
 *
 * 核心不变式放宽为「原文可以被修改，但每一处修改都必须显式声明、可见、可审计」
 * 之后，「改动」本身成了方案里的一等数据（docmodel.Edit）。校对区的接受/拒绝/
 * 手工新增都落在这里，每个函数返回新对象而不就地修改，理由与 tree.ts 一致：
 * 不会出现半改状态被 PUT 出去。
 *
 * 服务端是唯一裁决者：本文件只**编排**改动，从不决定它是否合法。是否生效由
 * assemble 应用、由 VerifyVerbatim 判定，结果通过 render 的 diff 回传。
 */

import type { Edit, EditKind, Plan } from '../api/types'

/** 方案里声明过的全部改动（缺失时为空的，调用方不必写 ?? []）。 */
export function planEdits(plan: Plan | null | undefined): Edit[] {
  return plan?.edits ?? []
}

/** 声明了几处改动。 */
export function editCount(plan: Plan | null | undefined): number {
  return planEdits(plan).length
}

/** 一条改动在 plan.edits 里的位置。按 index 定位是服务端的契约（拒绝=按下标删）。 */
export interface IndexedEdit {
  edit: Edit
  /** 在 plan.edits 里的下标，删除/替换时用。 */
  index: number
}

/** 全部改动，带下标，按声明顺序。 */
export function allIndexedEdits(plan: Plan | null | undefined): IndexedEdit[] {
  return planEdits(plan).map((edit, index) => ({ edit, index }))
}

/** 命中某节点的改动，带下标。 */
export function indexedEditsFor(plan: Plan | null | undefined, nodeId: string): IndexedEdit[] {
  const out: IndexedEdit[] = []
  planEdits(plan).forEach((edit, index) => {
    if (edit.node_id === nodeId) out.push({ edit, index })
  })
  return out
}

/** 没有任何节点指向的改动（例如模型给的 EditHeading 插入式改动）。 */
export function orphanEdits(plan: Plan | null | undefined): IndexedEdit[] {
  return allIndexedEdits(plan).filter((e) => (e.edit.node_id ?? '') === '')
}

/**
 * 一条改动的稳定标识。
 *
 * 用来记住「用户已看过/已接受」这类界面状态。刻意不用下标：删掉一条之后
 * 后面的下标全会移位，用下标做键会让「已接受」标记跳到别的改动上。
 */
export function editKey(edit: Edit): string {
  return [edit.node_id ?? '', edit.kind, edit.before, edit.occurrence ?? 0].join('\u0000')
}

/**
 * 把一条改动整理成可以提交的形状。
 *
 * before 是逐字匹配的锚点，**绝不能 trim**：原文里的全角空格与行尾空格都是
 * 内容的一部分，替用户去掉首尾空白会让 before 在原文中找不到，改动随即失效。
 * after 与 reason 同理只做「是不是全空」的判断，不做修剪。
 */
export function normalizeEdit(edit: Edit): Edit {
  const out: Edit = {
    kind: edit.kind,
    before: edit.before,
    after: edit.after,
  }
  if ((edit.node_id ?? '') !== '') out.node_id = edit.node_id
  if ((edit.reason ?? '') !== '') out.reason = edit.reason
  if (edit.occurrence !== undefined && edit.occurrence > 0) out.occurrence = edit.occurrence
  return out
}

/** 追加一条改动（返回新方案）。 */
export function addEdit(plan: Plan, edit: Edit): Plan {
  return { ...plan, edits: [...planEdits(plan), normalizeEdit(edit)] }
}

/**
 * 按下标删除一条改动 —— 这就是「拒绝」。
 *
 * 拒绝不是撤销操作，而是**撤回声明**：声明一旦撤掉，那段文字就不再被允许
 * 改动，成品里会保持原文（若有出入，VerifyVerbatim 立刻按「未声明改写」报错）。
 * 因此拒绝之后必须重新 PUT plan 校验，绝不能在本地默默了事。
 */
export function removeEditAt(plan: Plan, index: number): Plan {
  const edits = planEdits(plan).filter((_, i) => i !== index)
  return { ...plan, edits }
}

/** 按下标替换一条改动（手工修正模型给错的 before/after）。 */
export function replaceEditAt(plan: Plan, index: number, edit: Edit): Plan {
  const edits = planEdits(plan).map((cur, i) => (i === index ? normalizeEdit(edit) : cur))
  return { ...plan, edits }
}

/** 删掉全部改动（一键退回「原文逐字不变」）。 */
export function clearEdits(plan: Plan): Plan {
  return { ...plan, edits: [] }
}

// ------------------------------------------------- 展示用的文本重演（只读）

/**
 * nthIndex 返回 sub 第 n 次（0 基）出现的字节位置，找不到返回 -1。
 *
 * 这是 internal/docmodel.nthIndex 的镜像。刻意逐行对齐它的推进方式（每次
 * 前进 1 个字符而不是 len(sub)），这样「的的」这类重叠候选也能被找到 ——
 * 若这里的算法与服务端不同，界面显示的成品就会与真正导出的不同。
 */
function nthIndex(s: string, sub: string, occurrence: number): number {
  if (sub === '') return -1
  let from = 0
  for (let i = 0; ; i++) {
    const j = s.indexOf(sub, from)
    if (j < 0) return -1
    if (i === (occurrence < 0 ? 0 : occurrence)) return j
    from = j + 1
  }
}

/** before 能否在该节点文本中找到（决定这条改动是否会生效）。 */
export function beforeFound(nodeText: string, edit: Edit): boolean {
  return edit.before !== '' && nthIndex(nodeText, edit.before, edit.occurrence ?? 0) >= 0
}

/**
 * 把一组改动应用到一段文本上，返回结果与未能生效的条目。
 *
 * **这是只读的展示重演，服务端才是唯一裁决者。** 用途只有一个：让用户在
 * 校对区看到「这一段最终会变成什么样」，而不是逐条比对 before/after。
 * 若这里的行为与 internal/docmodel.AppliedText 有任何出入，以服务端为准 ——
 * 导出结果里的 diff.changed / diff.unapplied 才是权威。
 */
export function applyEdits(nodeText: string, edits: Edit[]): { text: string; failed: Edit[] } {
  let out = nodeText
  const failed: Edit[] = []
  for (const edit of edits) {
    if (edit.before === '') {
      // 插入式改动（新增标题）由装配器决定落点，这里不臆测。
      failed.push(edit)
      continue
    }
    const at = nthIndex(out, edit.before, edit.occurrence ?? 0)
    if (at < 0) {
      failed.push(edit)
      continue
    }
    out = out.slice(0, at) + edit.after + out.slice(at + edit.before.length)
  }
  return { text: out, failed }
}

/** 一条改动是否会真正改变文字（before === after 的改动不算改动）。 */
export function editChangesText(edit: Edit): boolean {
  return edit.before !== edit.after
}

/**
 * 拆出一段文本里被某条改动替换的部分，供界面做「红删除线 → 绿新增」的行内对照。
 *
 * 返回切点而不是 HTML：Vue 模板里用三个相邻元素渲染，避免任何 v-html，
 * 用户上传的原文就不会被当成标记解释。
 */
export interface InlineDiff {
  /** 改动之前的原文前缀。 */
  head: string
  /** 生效后的整段文本。 */
  result: string
}

/** 一条改动在节点内首次出现的位置，找不到返回 -1。 */
export function editOffset(nodeText: string, edit: Edit): number {
  return nthIndex(nodeText, edit.before, edit.occurrence ?? 0)
}

/** 把改动类型收敛成已知集合，未知值原样返回给界面显示。 */
export function asEditKind(kind: string): EditKind | 'unknown' {
  switch (kind) {
    case 'typo':
    case 'grammar':
    case 'wording':
    case 'punctuation':
    case 'sentence_split':
    case 'heading':
    case 'other':
      return kind
    default:
      return 'unknown'
  }
}
