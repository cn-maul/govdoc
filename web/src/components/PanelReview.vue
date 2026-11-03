<script setup lang="ts">
/**
 * 校对与输出区 —— 定位为**可选的高级校对**。
 *
 * 用户要求「不用再人工校对」，因此主流程（分析区）在方案通过校验后直接生成并
 * 下载；这里承担的是「想核对/想改」时才进来的深度界面。默认不阻塞导出。
 *
 * 模型的三类问题要在这里被看见或修好：
 *   1. **漏收**：某段原文没有被任何章节引用（uncovered_content，error）。
 *      左栏该节点标红，用户可用「收录到…」把它插入任一章节。
 *   2. **重复**：同一段原文被两个章节同时引用（duplicate_content，error）。
 *      左栏该节点标黄，用户可点「只保留一处」，或把多余那处「移出」。
 *   3. **改动**：模型声明要改原文（plan.edits）。
 *      每条都就地显示 before → after（红删除线/绿新增）+ 类型徽标 + 理由，
 *      并可**逐条接受或拒绝**。拒绝 = 从 plan.edits 按下标删除该条，随即 PUT
 *      plan 重新校验 —— 声明撤掉后那段文字就不许再变，成品会保持原文。
 *      也可手工新增一条改动。
 *
 * 另有「标题与原文不一致」（heading_mismatch）：章节标题引用了原文标题节点时，
 * 标题必须与原文逐字相同；用户改标题后要么改回去，要么解除与该节点的关联，
 * 使其成为可自由书写的自拟标题。
 *
 * 每一次编辑都经 editPlan()：本地立即生效 → 防抖 700ms → PUT /api/documents/{id}/plan
 * 重新校验 → 刷新 report。校验未通过时导出按钮被禁用，并逐条列出原因。
 */
import { computed, onMounted, reactive, ref } from 'vue'
import {
  EDIT_KINDS,
  KIND_LABEL,
  ROLE_LABEL,
  editLabel,
  reportSummary,
  type Edit,
  type EditKind,
  type NodeSummary,
  type Role,
  type Section,
} from '../api/types'
import {
  appendSection,
  attachPreamble,
  attachSectionHeading,
  claimSites,
  consolidateNode,
  countBodyRefs,
  dedupeAllNodes,
  detachBodyNode,
  detachElement,
  detachPreamble,
  detachSectionHeading,
  elementRoleOf,
  flattenSections,
  moveBodyNode,
  movePreamble,
  placeBodyNodeInSourceOrder,
  preambleHas,
  removeClaimSite,
  setSectionTitle,
  walkSections,
  type ClaimSite,
} from '../lib/tree'
import {
  addEdit,
  applyEdits,
  beforeFound,
  clearEdits,
  editKey,
  orphanEdits,
  removeEditAt,
  type IndexedEdit,
} from '../lib/edits'
import { blockReasons, issuesByNode, nodeStatus, type NodeIssue } from '../lib/report'
import {
  downloadRenderedFile,
  editPlan,
  gov,
  loadSource,
  notify,
  renderDocument,
  verifyPlanNow,
} from '../composables/useGovdoc'

const emit = defineEmits<{ (e: 'preview'): void }>()

const docId = computed(() => gov.upload?.doc_id ?? '')

onMounted(() => {
  if (docId.value !== '') void loadSource(docId.value)
})

// ---------------------------------------------------------------- 原文左栏

type NodeFilter = 'all' | 'missing' | 'duplicate' | 'heading' | 'preamble' | 'edited'

const nodeFilter = ref<NodeFilter>('all')
const keyword = ref('')

const issues = computed(() => issuesByNode(gov.report))

/**
 * 原文节点索引：id → 节点。
 *
 * 方案树里每一处引用都要显示原文文本，逐条 array.find 会让大的方案变成
 * O(n·m)；建一次索引把渲染压回线性。
 */
const nodeIndex = computed(() => {
  const map = new Map<string, NodeSummary>()
  for (const n of gov.source?.doc.nodes ?? gov.upload?.nodes ?? []) map.set(n.id, n)
  return map
})

/**
 * 章节索引：section id → 章节。
 *
 * headingNodeOwner / sectionsContainingNode 是 O(树)，在 v-for 里逐行调用
 * 会重复遍历整棵树；这里一次遍历建表。
 */
const sectionIndex = computed(() => {
  const byId = new Map<string, Section>()
  const byHeading = new Map<string, Section>()
  const bodyOf = new Map<string, Section[]>()
  walkSections(gov.plan?.sections ?? [], (s) => {
    byId.set(s.id, s)
    if (s.heading_node_id) byHeading.set(s.heading_node_id, s)
    for (const id of s.body_node_ids ?? []) {
      const list = bodyOf.get(id)
      if (list) list.push(s)
      else bodyOf.set(id, [s])
    }
  })
  return { byId, byHeading, bodyOf }
})

// ---------------------------------------------------------------- 改动索引

/**
 * 全部改动，带在 plan.edits 里的下标。
 *
 * 下标是「拒绝」的依据：服务端契约就是按下标删除该条，因此必须在整张表上
 * 保留原始下标，而不是在某节点过滤后的子表里另算。
 */
const indexedEditsAll = computed(() => {
  const out: IndexedEdit[] = []
  ;(gov.plan?.edits ?? []).forEach((edit, index) => out.push({ edit, index }))
  return out
})

/**
 * 一个节点上挂的改动（带在 plan.edits 里的下标）。
 *
 * 用 O(1) 索引而不是在每个节点里逐条扫描 plan.edits：大文档的节点数可能上千，
 * 每个节点都遍历一遍改动表会让渲染退化成平方复杂度。
 */
const editsByNode = computed(() => {
  const map = new Map<string, IndexedEdit[]>()
  for (const item of indexedEditsAll.value) {
    const id = item.edit.node_id ?? ''
    if (id === '') continue
    const list = map.get(id)
    if (list) list.push(item)
    else map.set(id, [item])
  }
  return map
})

/** 某节点上挂的改动。 */
function editsOf(nodeId: string): IndexedEdit[] {
  return editsByNode.value.get(nodeId) ?? []
}

/** 该节点带的改动类型徽标（去重，按声明顺序）。 */
function editKindBadges(nodeId: string): string[] {
  const seen = new Set<string>()
  for (const { edit } of editsOf(nodeId)) seen.add(edit.kind)
  return [...seen]
}

/**
 * 该节点的文本按改动重演之后的结果；无改动时返回 null。
 *
 * 这是唯一一处「本地预览成品」的地方，与服务端的 AppliedText 互为镜像。
 * 若二者有出入，以服务端为准 —— 导出结果里的 diff.changed / diff.unapplied
 * 才是权威结论。
 */
interface NodeRender {
  /** 原文。 */
  before: string
  /** 应用全部改动后的文本。 */
  after: string
  /** 未能生效的改动条数（before 在原文里找不到）。 */
  failed: number
  /** 是否有改动真正改变了文字。 */
  changed: boolean
}

function renderedOf(items: IndexedEdit[], nodeText: string): NodeRender | null {
  if (items.length === 0) return null
  const { text, failed } = applyEdits(
    nodeText,
    items.map((i) => i.edit),
  )
  const changed = text !== nodeText
  return { before: nodeText, after: text, failed: failed.length, changed }
}

interface NodeRow {
  node: NodeSummary
  status: ReturnType<typeof nodeStatus>
  issue: NodeIssue | undefined
  /** 引用它的章节（正文位置）。 */
  inSections: Section[]
  /** 被用作某章节标题的节点。 */
  headingOf: Section | null
  /** 被绑定到的公文要素槽位（标题/成文日期/抄送…），未绑定为 null。 */
  elementRole: string | null
  /**
   * 该节点被认领的全部位置（要素绑定 / 正文前言 / 章节标题 / 章节正文）。
   * 多于一处就是 VerifyPlan 报的 duplicate_content；界面据此给出「只保留第一处」。
   */
  claims: ClaimSite[]
  /** 是否属于「正文」节点（page_break 不参与覆盖统计）。 */
  counts: boolean
  /** 该节点上挂的改动（带 plan.edits 下标），无改动时为空数组。 */
  edits: IndexedEdit[]
  /**
   * 应用本节点全部改动后的整段结果；无改动时为 null。
   *
   * 在 computed 里算一次，而不是在模板里反复调用函数：模板每次重渲染都会
   * 重新求值，大文档下会平白多出成倍的字符串拼接。
   */
  rendered: NodeRender | null
}

const nodeRows = computed<NodeRow[]>(() => {
  const nodes = gov.source?.doc.nodes ?? gov.upload?.nodes ?? []
  const kw = keyword.value.trim()
  const idx = sectionIndex.value
  const plan = gov.plan

  return nodes
    .filter((n) => {
      const status = nodeStatus(issues.value.get(n.id))
      switch (nodeFilter.value) {
        case 'missing':
          return status === 'missing'
        case 'duplicate':
          return status === 'duplicate'
        case 'heading':
          return idx.byHeading.has(n.id)
        case 'preamble':
          return preambleHas(plan, n.id)
        case 'edited':
          // 「有改动」按 node_id 命中：这样筛选出来的是「模型想动哪些段」，
          // 正是用户复核改动时最需要的那一屏。
          return editsByNode.value.has(n.id)
        default:
          return true
      }
    })
    .filter(
      (n) =>
        kw === '' ||
        n.text.includes(kw) ||
        n.id.includes(kw) ||
        // 也按改动的文字搜索：用户常记得「改成了什么」，记不住节点号。
        editsOf(n.id).some(
          (e) =>
            e.edit.before.includes(kw) ||
            e.edit.after.includes(kw) ||
            (e.edit.reason ?? '').includes(kw),
        ),
    )
    .map((n) => {
      const nodeEdits = editsByNode.value.get(n.id) ?? []
      return {
        node: n,
        status: nodeStatus(issues.value.get(n.id)),
        issue: issues.value.get(n.id),
        inSections: idx.bodyOf.get(n.id) ?? [],
        headingOf: idx.byHeading.get(n.id) ?? null,
        elementRole: elementRoleOf(plan, n.id),
        claims: plan ? claimSites(plan, n.id) : [],
        counts: n.kind !== 'page_break',
        edits: nodeEdits,
        rendered: renderedOf(nodeEdits, n.text),
      }
    })
})

/** 原文中携带正文的节点总数（page_break 不计入覆盖统计）。 */
const contentTotal = computed(() => gov.report?.content_nodes ?? gov.upload?.content_count ?? 0)

/** 左栏筛选按钮上的计数。 */
const nodeCounts = computed(() => {
  let missing = 0
  let duplicate = 0
  for (const [, issue] of issues.value) {
    const st = nodeStatus(issue)
    if (st === 'missing') missing++
    if (st === 'duplicate') duplicate++
  }
  // 带改动的节点数（一个节点可能有多条改动）。
  let edited = 0
  for (const n of gov.source?.doc.nodes ?? gov.upload?.nodes ?? []) {
    if (editsByNode.value.has(n.id)) edited++
  }
  return { missing, duplicate, edited }
})

// ---------------------------------------------------------------- 改动汇总

/** 方案里声明的改动总数。 */
const editTotal = computed(() => indexedEditsAll.value.length)

/** 没有任何节点指向的改动（EditHeading 插入式），单独列出以免用户看不到。 */
const unattachedEdits = computed(() => orphanEdits(gov.plan))

/** 改动汇总：按类型计数，用于顶部概览徽标。 */
const editKindCounts = computed(() => {
  const map = new Map<string, number>()
  for (const { edit } of indexedEditsAll.value) map.set(edit.kind, (map.get(edit.kind) ?? 0) + 1)
  return [...map.entries()]
})

/**
 * 拒绝一条改动 —— 按下标把它从 plan.edits 里删掉。
 *
 * 拒绝的语义是**撤回声明**，不是「撤销一次编辑」：声明一旦撤掉，那段文字就不
 * 再被允许改动，成品会输出原文；若模型已经把别处的文字改了，VerifyVerbatim
 * 立刻会按「未声明的改写」报错。因此必须随后 PUT plan 重新校验，绝不能只在
 * 本地默默了事 —— 用户需要看到拒绝之后的真实校验结论。
 */
function rejectEdit(index: number, edit: Edit): void {
  editPlan((plan) => removeEditAt(plan, index))
  notify(
    'info',
    `已拒绝这条改动（${editLabel(edit.kind)}）：原文「${clip(edit.before)}」将保持不动。正在重新校验。`,
  )
}

/**
 * 接受一条改动。
 *
 * 改动本身在 plan.edits 里就已经「生效」了（装配器逐条应用它），所以「接受」
 * 不是一个状态翻转，而是**把用户看到的内容再确认一次并提交校验**：真正让改动
 * 落进成文的是那份被校验过的方案。这里显式提交，是为了让「接受」有个可验证的
 * 结果（校验结论刷新），而不是一个什么也不做的按钮。
 */
async function acceptEdit(edit: Edit): Promise<void> {
  await verifyPlanNow()
  notify('ok', `已接受这条改动（${editLabel(edit.kind)}），方案已重新校验。`)
}

/** 拒绝全部改动：一键退回「原文逐字不变」。 */
async function rejectAllEdits(): Promise<void> {
  const n = editTotal.value
  if (n === 0) {
    notify('info', '当前没有任何改动。')
    return
  }
  editPlan((plan) => clearEdits(plan))
  notify('info', `已拒绝全部 ${n} 处改动，原文将逐字不变。正在重新校验。`)
}

/**
 * 接受全部改动：逐条都已生效，这里只需一次显式校验。
 * 用一次提交而不是 N 次点击，是因为校验的结论本来就是整体性的。
 */
async function acceptAllEdits(): Promise<void> {
  if (editTotal.value === 0) {
    notify('info', '当前没有任何改动。')
    return
  }
  const ok = await verifyPlanNow()
  notify(
    ok ? 'ok' : 'info',
    ok
      ? `已接受全部 ${editTotal.value} 处改动，方案校验通过。`
      : `已提交全部 ${editTotal.value} 处改动，但方案仍未通过校验，请看下方原因。`,
  )
}

/** 截断长文本用于提示。 */
function clip(s: string, n = 24): string {
  return s.length <= n ? s : `${s.slice(0, n)}…`
}

// ------------------------------------------------- 手工新增改动

/**
 * 新增改动表单。
 *
 * node_id 与 before 是关键：before 必须**逐字**取自原文（含全角空格），所以
 * 表单里提供一个「用该节点原文填入」的按钮，并允许粘贴。手动敲错一个字，
 * 这条改动就会因为 before 找不到而不生效 —— 服务端会在 diff.unapplied 里点出来。
 */
const newEdit = reactive({
  nodeId: '',
  kind: 'typo' as EditKind,
  before: '',
  after: '',
  reason: '',
  occurrence: 0,
})

/** 可被改动的节点：携带正文、且有文本的节点。 */
const editableNodes = computed(() =>
  (gov.source?.doc.nodes ?? gov.upload?.nodes ?? []).filter(
    (n) => n.kind !== 'page_break' && (n.text ?? '') !== '',
  ),
)

/** 新增表单里选中节点的原文，用于「填入原文」与实时校验 before。 */
const newEditNodeText = computed(() => nodeText(newEdit.nodeId))

/** before 是否能在所选节点里找到 —— 找不到这条改动一定不生效，提前告知。 */
const newEditBeforeFound = computed(() => {
  if (newEdit.nodeId === '' || newEdit.before === '') return true
  return beforeFound(newEditNodeText.value, {
    kind: newEdit.kind,
    before: newEdit.before,
    after: newEdit.after,
  })
})

/** 表单是否可以提交。 */
const newEditValid = computed(
  () =>
    newEdit.nodeId !== '' &&
    newEdit.before !== '' &&
    newEdit.before !== newEdit.after &&
    newEditBeforeFound.value,
)

/** 把所选节点的原文填进 before（逐字，不做任何修剪）。 */
function fillBeforeFromNode(): void {
  newEdit.before = newEditNodeText.value
}

/** 提交一条手工改动。 */
function submitNewEdit(): void {
  if (newEdit.nodeId === '') {
    notify('error', '请先选择要修改的原文节点。')
    return
  }
  if (newEdit.before === '') {
    notify('error', '请填写「原文内容」（before），它必须与原文逐字一致。')
    return
  }
  if (newEdit.before === newEdit.after) {
    notify('error', '「改为」与「原文内容」相同，这条改动不会产生任何效果。')
    return
  }
  if (!newEditBeforeFound.value) {
    notify('error', '「原文内容」在所选节点里找不到，这条改动不会生效。请核对（注意全角空格也要一致）。')
    return
  }

  const edit: Edit = {
    node_id: newEdit.nodeId,
    kind: newEdit.kind,
    before: newEdit.before,
    after: newEdit.after,
    reason: newEdit.reason,
    occurrence: newEdit.occurrence,
  }
  editPlan((plan) => addEdit(plan, edit))
  notify('ok', `已新增一条改动（${editLabel(edit.kind)}），正在重新校验。`)
  newEdit.before = ''
  newEdit.after = ''
  newEdit.reason = ''
  newEdit.occurrence = 0
}

// ------------------------------------------------- 成文结果的改动清单

/** 成文后已生效的改动（服务端权威结论）。 */
const appliedEdits = computed(() => gov.render?.diff?.changed ?? [])

/** 成文后未能生效的改动。 */
const unappliedEdits = computed(() => gov.render?.diff?.unapplied ?? [])

/** 成文后的逐字比对是否存在真实偏差（missing / altered）。 */
const diffHasProblems = computed(() => {
  const d = gov.render?.diff
  if (!d) return false
  return (d.missing?.length ?? 0) > 0 || (d.altered?.length ?? 0) > 0
})

// ---------------------------------------------------------------- 方案右栏

const flat = computed(() => flattenSections(gov.plan?.sections ?? []))

/** 所有可作为「移入目标」的章节。 */
const sectionOptions = computed(() =>
  flat.value.map((f) => ({ id: f.section.id, label: f.label, depth: f.depth })),
)

/** 每个漏收节点的「收录到哪个章节」选择（键：node id → section id）。 */
const collectTarget = ref<Record<string, string>>({})

/** 每个章节的「把哪个节点移入」选择（键：章节 id → 被移动的节点 id）。 */
const relocateNode = ref<Record<string, string>>({})

/** 每个章节的「移入到哪个章节」选择（键：章节 id → 目标 section id）。 */
const relocateTo = ref<Record<string, string>>({})

/** 每个章节的「关联哪段原文标题」选择（键：章节 id → node id）。 */
const attachNode = ref<Record<string, string>>({})

function targetFor(nodeId: string): string {
  return collectTarget.value[nodeId] ?? ''
}

function setCollectTarget(nodeId: string, sectionId: string): void {
  collectTarget.value = { ...collectTarget.value, [nodeId]: sectionId }
}

function pickNode(sectionId: string, nodeId: string): void {
  relocateNode.value = { ...relocateNode.value, [sectionId]: nodeId }
}

function pickRelocateTo(sectionId: string, targetId: string): void {
  relocateTo.value = { ...relocateTo.value, [sectionId]: targetId }
}

function pickHeading(sectionId: string, nodeId: string): void {
  attachNode.value = { ...attachNode.value, [sectionId]: nodeId }
}

/** 章节内当前选中的待移出节点（默认第一个）。 */
function movableOf(section: Section): string {
  return relocateNode.value[section.id] ?? section.body_node_ids?.[0] ?? ''
}

/** 章节当前选中的移入目标。 */
function relocateTargetOf(sectionId: string): string {
  return relocateTo.value[sectionId] ?? ''
}

/** 原文顺序表：id → Order。按原文顺序插入时用它决定插入位置。 */
const sourceOrder = computed(() => {
  const map = new Map<string, number>()
  for (const n of gov.source?.doc.nodes ?? gov.upload?.nodes ?? []) map.set(n.id, n.order)
  return map
})

/** 把漏收节点插入所选章节，插入位置按原文顺序（避免平白触发 reordered 警告）。 */
function collectNode(node: NodeSummary): void {
  const target = targetFor(node.id)
  if (target === '') {
    notify('error', '请先选择要把这一段收录到哪个章节。')
    return
  }
  editPlan((plan) => placeBodyNodeInSourceOrder(plan, target, node.id, sourceOrder.value))
  notify('ok', `已把 ${node.id} 按其原文位置收录到所选章节，正在重新校验。`)
}

/** 一次性把当前所有漏收的正文节点收进所选章节 —— 批量修复漏收。 */
function collectAllMissing(target: string): void {
  if (target === '') {
    notify('error', '请先选择要批量收录到哪个章节。')
    return
  }
  const missing = [...issues.value.entries()]
    .filter(([, issue]) => nodeStatus(issue) === 'missing')
    .map(([id]) => id)
    .sort((a, b) => (sourceOrder.value.get(a) ?? 0) - (sourceOrder.value.get(b) ?? 0))
  if (missing.length === 0) {
    notify('info', '当前没有漏收的正文节点。')
    return
  }
  editPlan((plan) => {
    let next = plan
    for (const id of missing) {
      next = placeBodyNodeInSourceOrder(next, target, id, sourceOrder.value)
    }
    return next
  })
  notify('ok', `已把 ${missing.length} 段漏收原文按其原文顺序收进所选章节，正在重新校验。`)
}

// ---------------------------------------------------------------- 正文前言

/** 正文前言里的节点，按前言顺序。 */
const preambleRows = computed(() => {
  const ids = gov.plan?.preamble_node_ids ?? []
  return ids.map((id, i) => ({ id, index: i, text: nodeText(id) }))
})

/** 把节点加入正文前言（移出它在别处的认领，故不会产生重复）。 */
function addToPreamble(node: NodeSummary): void {
  editPlan((plan) => attachPreamble(plan, node.id, sourceOrder.value))
  notify('ok', `已把 ${node.id} 移入「正文前言（无编号）」；前言不会编号，也不是章节。正在重新校验。`)
}

/** 从前言移出一个节点到指定章节。 */
function movePreambleTo(nodeId: string, sectionId: string): void {
  if (sectionId === '') {
    notify('error', '请先选择要移入哪个章节。')
    return
  }
  editPlan((plan) => placeBodyNodeInSourceOrder(plan, sectionId, nodeId, sourceOrder.value))
  notify('ok', `已把 ${nodeId} 从前言移入所选章节，正在重新校验。`)
}

/** 把节点从正文前言移除（不再由前言承载，会变成漏收，需另行收录）。 */
function removeFromPreamble(nodeId: string): void {
  editPlan((plan) => detachPreamble(plan, nodeId))
  notify('info', `已把 ${nodeId} 移出正文前言；若它仍属正文，请收录到某章节或前言。`)
}

/** 前言内上移/下移。 */
function movePreambleNode(nodeId: string, delta: -1 | 1): void {
  editPlan((plan) => movePreamble(plan, nodeId, delta))
}

/** 批量把全部漏收节点收进正文前言（前言段落往往就是漏收的那几段）。 */
function collectAllToPreamble(): void {
  const missing = [...issues.value.entries()]
    .filter(([, issue]) => nodeStatus(issue) === 'missing')
    .map(([id]) => id)
    .sort((a, b) => (sourceOrder.value.get(a) ?? 0) - (sourceOrder.value.get(b) ?? 0))
  if (missing.length === 0) {
    notify('info', '当前没有漏收的正文节点。')
    return
  }
  editPlan((plan) => {
    let next = plan
    for (const id of missing) next = attachPreamble(next, id, sourceOrder.value)
    return next
  })
  notify('ok', `已把 ${missing.length} 段漏收原文收进「正文前言（无编号）」，正在重新校验。`)
}

/**
 * 「只保留第一处」：把一个节点的多处认领合并为一处。
 *
 * 必须跨三种认领位置操作（要素绑定 / 章节标题 / 章节正文）：模型常把同一段
 * 既绑成「标题」又收进章节正文，只在正文里删一处是修不掉的。
 */
function keepFirstOnly(node: NodeSummary): void {
  let sites: ClaimSite[] = []
  editPlan((plan) => {
    sites = claimSites(plan, node.id)
    if (sites.length <= 1) return plan
    return consolidateNode(plan, node.id, sites[0])
  })
  if (sites.length > 1) {
    notify('ok', `已把 ${node.id} 的 ${sites.length} 处认领合并保留为「${sites[0].label}」，正在重新校验。`)
  }
}

/** 去掉该节点指定的那一处认领（用于逐个「移出第 N 处」）。 */
function dropClaim(nodeId: string, site: ClaimSite): void {
  editPlan((plan) => removeClaimSite(plan, nodeId, site))
  notify('info', `已去掉 ${nodeId} 在「${site.label}」的收录，正在重新校验。`)
}

/** 把节点从某一章节移出（不删除原文，只是不再收录）。 */
function detach(sectionId: string, nodeId: string): void {
  editPlan((plan) => ({ ...plan, sections: detachBodyNode(plan.sections, sectionId, nodeId) }))
}

/**
 * 解除节点的公文要素绑定。
 *
 * 这是「漏收」的一种隐藏成因：节点被绑成了要素（或绑错了槽位）就不再计入正文
 * 覆盖。用户解开绑定后即可把它作为正文收录进章节。
 */
function unbindElement(nodeId: string): void {
  editPlan((plan) => detachElement(plan, nodeId))
  notify('info', `已解除 ${nodeId} 的公文要素绑定；若它其实是正文，请用「收录」把它放进章节。`)
}

/** 章节内上移 / 下移。 */
function move(sectionId: string, nodeId: string, delta: -1 | 1): void {
  editPlan((plan) => ({ ...plan, sections: moveBodyNode(plan.sections, sectionId, nodeId, delta) }))
}

/** 把方案树里的一处引用移到另一个章节。 */
function relocate(fromSectionId: string, nodeId: string, toSectionId: string): void {
  if (toSectionId === '' || toSectionId === fromSectionId) return
  editPlan((plan) => placeBodyNodeInSourceOrder(plan, toSectionId, nodeId, sourceOrder.value))
  notify('ok', `已把 ${nodeId} 移入所选章节，正在重新校验。`)
}

/** 编辑章节标题（会触发 heading_mismatch，界面给出下一步提示）。 */
function rename(sectionId: string, value: string): void {
  editPlan((plan) => ({ ...plan, sections: setSectionTitle(plan.sections, sectionId, value) }))
}

/** 解除章节标题与原文节点的关联，使其成为自拟标题。 */
function detachHeading(sectionId: string): void {
  editPlan((plan) => ({ ...plan, sections: detachSectionHeading(plan.sections, sectionId) }))
  notify('info', '该章节标题已改为自拟标题，可自由编辑；原文标题节点将回到未收录状态。')
}

/** 把原文标题节点关联回章节，并把标题同步为原文。 */
function attachHeading(sectionId: string, nodeId: string): void {
  const text = nodeText(nodeId)
  editPlan((plan) => ({ ...plan, sections: attachSectionHeading(plan.sections, sectionId, nodeId, text) }))
}

/** 把章节标题改回原文标题节点的文本，一键修好 heading_mismatch。 */
function restoreHeadingTitle(sectionId: string): void {
  const sec = flat.value.find((f) => f.section.id === sectionId)?.section
  if (!sec?.heading_node_id) return
  const text = nodeText(sec.heading_node_id)
  editPlan((plan) => ({ ...plan, sections: setSectionTitle(plan.sections, sectionId, text) }))
  notify('ok', '标题已恢复为原文文本。')
}

/** 某个节点的原文文本（经索引，O(1)）。 */
function nodeText(id: string): string {
  return nodeIndex.value.get(id)?.text ?? ''
}

/** 新建章节：把尚未归属的段落有个去处。 */
const newTitle = ref('')

function addSection(): void {
  const title = newTitle.value.trim()
  if (title === '') {
    notify('error', '请先填写新章节的标题。')
    return
  }
  let createdId = ''
  editPlan((plan) => {
    const res = appendSection(plan.sections, title)
    createdId = res.id
    return { ...plan, sections: res.sections }
  })
  newTitle.value = ''
  notify('ok', `已新建章节「${title}」（${createdId}），它是自拟标题，可继续把原文段落收录进来。`)
}

/** 章节是否含 error 级问题。 */
function sectionHasError(s: Section): boolean {
  const report = gov.report
  if (!report) return false
  const ids = new Set<string>([
    ...(s.body_node_ids ?? []),
    ...(s.heading_node_id ? [s.heading_node_id] : []),
  ])
  return (report.diagnostics ?? []).some(
    (d) => d.severity === 'error' && (d.code === 'bad_level' || d.node_ids?.some((i) => ids.has(i) || i === s.id)),
  )
}

// ---------------------------------------------------------------- 校验与导出

const reasons = computed(() => blockReasons(gov.report))
const canExport = computed(
  () => gov.plan !== null && !gov.dirty && reasons.value.length === 0 && !gov.rendering,
)

/**
 * 「下载公文文档」的可用性。
 *
 * 与 canExport 不同：canExport 要求本地没有未提交的改动，而下载用的是**已经
 * 生成好的文件**，所以本地是否 dirty 不影响它能不能下 —— 只要求那份文件仍与
 * 当前校验结论一致（report 通过）。否则用户会把一份与屏幕上方案已不符的旧文件
 * 当成合规交付物拿走。
 */
const downloadReady = computed(() => gov.render !== null && reasons.value.length === 0)

/** 下载按钮不可用时的中文原因。 */
const downloadWhy = computed(() => {
  if (gov.render === null) return '还没有生成公文文档，请先点「生成公文文档」。'
  if (reasons.value.length > 0) return '当前方案未通过校验，已暂停下载。请先修好下方问题。'
  return ''
})

/** 是否已经下载过这份文件（按钮保持可用，仅作提示）。 */
const alreadyDownloaded = computed(
  () => gov.render !== null && gov.downloaded.includes(gov.render.file_id),
)

async function revalidate(): Promise<void> {
  await verifyPlanNow()
}

async function exportDocx(): Promise<void> {
  await renderDocument(docId.value)
}

/** 「下载公文文档」主按钮：直接触发 GET /api/files/{file_id}。 */
function downloadDocx(): void {
  downloadRenderedFile()
}

const refCount = computed(() => countBodyRefs(gov.plan?.sections ?? []))

/** 可作为章节标题关联对象的原文标题节点。 */
const sourceHeadings = computed(() =>
  (gov.source?.doc.nodes ?? gov.upload?.nodes ?? []).filter((n) => n.kind === 'heading'),
)

/** 批量收录漏收节点时的目标章节；`__preamble__` 表示正文前言。 */
const bulkTarget = ref('')

function bulkCollect(): void {
  if (bulkTarget.value === '__preamble__') {
    collectAllToPreamble()
    return
  }
  collectAllMissing(bulkTarget.value)
}

/** 批量把多处认领的节点各合并为一处（修复模型重复收录）。 */
function dedupeAll(): void {
  // 候选来自两处：界面已算出的 claims（覆盖「要素绑定 + 章节正文」这类
  // 报告未必点名的组合）与报告点名的 duplicate_content 节点。
  const candidates = new Set<string>()
  for (const row of nodeRows.value) {
    if (row.claims.length > 1) candidates.add(row.node.id)
  }
  for (const d of gov.report?.diagnostics ?? []) {
    if (d.code === 'duplicate_content') for (const id of d.node_ids ?? []) candidates.add(id)
  }

  let merged = 0
  editPlan((plan) => {
    const ids = [...candidates].filter((id) => claimSites(plan, id).length > 1)
    merged = ids.length
    return dedupeAllNodes(plan, ids)
  })

  if (merged === 0) {
    notify('info', '当前没有重复收录的原文节点。')
    return
  }
  notify('ok', `已合并 ${merged} 段重复收录，正在重新校验。`)
}
</script>

<template>
  <section class="gd-work">
    <header class="gd-section-title">
      <h2>校对与输出</h2>
      <p>
        可选的高级校对：逐段核对原文是否都被收录且只收录一次，并逐条审阅模型声明的改动。
        任何修改都会自动提交校验；校验通过后才能成文。
      </p>
    </header>

    <div v-if="!gov.plan" class="gd-card">
      <div class="gd-empty">
        <div>还没有结构方案。</div>
        <div class="gd-muted">请先在「分析文档结构」区点击「开始分析」。</div>
      </div>
    </div>

    <template v-else>
      <!--
        顶部操作条 —— 「下载公文文档」的显著位置之一。
        用户明确说过「少了个最终下载文档的按钮」，因此只要文件已经生成且校验
        仍然通过，这个主按钮就常驻在这里，无论滚到校对的哪一段都能看见。
      -->
      <div v-if="gov.render" class="gd-card gd-card--compact gd-card--secondary">
        <div class="gd-row gd-row--between">
          <div class="gd-row">
            <span class="gd-badge" :class="downloadReady ? 'gd-badge--success' : 'gd-badge--danger'">
              <span class="gd-dot" aria-hidden="true"></span>
              {{ downloadReady ? '公文文档已生成' : '文件已生成但方案已变更' }}
            </span>
            <span class="gd-muted" style="font-size: 13px">{{ gov.render.filename }}</span>
            <span v-if="alreadyDownloaded" class="gd-badge">已下载过</span>
          </div>
          <div class="gd-row">
            <button
              type="button"
              class="gd-btn gd-btn--sm gd-btn--ghost"
              :disabled="gov.rendering || !canExport"
              :title="canExport ? '' : '方案有未提交的修改或校验未通过'"
              @click="exportDocx"
            >
              重新生成
            </button>
            <button
              type="button"
              class="gd-btn gd-btn--primary"
              :disabled="!downloadReady"
              :title="downloadWhy"
              @click="downloadDocx"
            >
              下载公文文档
            </button>
          </div>
        </div>
        <div v-if="!downloadReady && downloadWhy !== ''" class="gd-field__hint">{{ downloadWhy }}</div>
      </div>

      <!-- 校验状态条 -->
      <div class="gd-card gd-card--compact">
        <div class="gd-row gd-row--between">
          <div class="gd-row">
            <span
              class="gd-badge"
              :class="reasons.length === 0 ? 'gd-badge--success' : 'gd-badge--danger'"
            >
              <span class="gd-dot" aria-hidden="true"></span>
              {{ reasons.length === 0 ? '校验通过' : '校验未通过' }}
            </span>
            <span class="gd-muted" style="font-size: 13px">{{ reportSummary(gov.report) }}</span>
            <span v-if="gov.dirty" class="gd-badge gd-badge--warning">有未提交的修改</span>
            <span v-else-if="gov.verifying" class="gd-badge gd-badge--accent">
              <span class="gd-spinner" aria-hidden="true"></span> 校验中
            </span>
          </div>
          <div class="gd-row">
            <span class="gd-muted" style="font-size: 12px">
              章节 {{ flat.length }} 个 · 正文引用 {{ refCount }} 处
              <template v-if="(gov.plan?.preamble_node_ids ?? []).length > 0">
                · 前言 {{ (gov.plan?.preamble_node_ids ?? []).length }} 段
              </template>
              <template v-if="(gov.report?.bound_elements ?? 0) > 0">
                · 公文要素 {{ gov.report?.bound_elements }}
              </template>
              <template v-if="editTotal > 0"> · 改动 {{ editTotal }} 处</template>
            </span>
            <button
              type="button"
              class="gd-btn gd-btn--sm gd-btn--ghost"
              :disabled="gov.verifying"
              @click="revalidate"
            >
              重新校验
            </button>
            <button
              type="button"
              class="gd-btn gd-btn--sm gd-btn--secondary"
              :disabled="!canExport"
              :title="reasons.length > 0 ? '校验未通过，无法成文' : ''"
              @click="exportDocx"
            >
              <span v-if="gov.rendering" class="gd-spinner" aria-hidden="true"></span>
              {{ gov.rendering ? '成文中…' : gov.render ? '重新生成公文文档' : '生成公文文档' }}
            </button>
            <button
              v-if="gov.render"
              type="button"
              class="gd-btn gd-btn--sm gd-btn--primary"
              :disabled="!downloadReady"
              :title="downloadWhy"
              @click="downloadDocx"
            >
              下载公文文档
            </button>
          </div>
        </div>

        <div v-if="reasons.length > 0" class="gd-alert gd-alert--danger">
          <span class="gd-alert__icon" aria-hidden="true">!</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">导出已禁用，请先修复以下问题</div>
            <div v-for="r in reasons" :key="r" class="gd-alert__list">· {{ r }}</div>
          </div>
        </div>

        <!-- 批量修复：模型漏收/重复一整批时，逐条点太慢 -->
        <div v-if="nodeCounts.missing > 0 || nodeCounts.duplicate > 0" class="gd-row">
          <span class="gd-muted" style="font-size: 12px">批量修复：</span>
          <select
            class="gd-select"
            style="max-width: 16rem; font-size: 12px; padding: 4px 2rem 4px 8px"
            :value="bulkTarget"
            @change="bulkTarget = ($event.target as HTMLSelectElement).value"
          >
            <option value="">把全部漏收段落收进…</option>
            <option value="__preamble__">正文前言（无编号）</option>
            <option v-for="o in sectionOptions" :key="o.id" :value="o.id">
              {{ o.depth > 0 ? '　'.repeat(o.depth) : '' }}{{ o.label }}
            </option>
          </select>
          <button
            type="button"
            class="gd-btn gd-btn--sm gd-btn--accent-soft"
            :disabled="bulkTarget === '' || nodeCounts.missing === 0"
            @click="bulkCollect"
          >
            收录 {{ nodeCounts.missing }} 段漏收
          </button>
          <button
            type="button"
            class="gd-btn gd-btn--sm gd-btn--warning-soft"
            :disabled="nodeCounts.duplicate === 0"
            @click="dedupeAll"
          >
            合并 {{ nodeCounts.duplicate }} 段重复
          </button>
        </div>
      </div>

      <!--
        改动汇总面板。
        两块内容不同且必须分开：
          · 方案声明的改动（plan.edits）—— 待用户审阅的**意向**，可逐条接受/拒绝；
          · 成文结果里的 diff.changed / diff.unapplied —— 服务端的**权威结论**，
            分别表示「真的改到了成文里」与「before 没找到、没生效」。
        把两者混在一起会让用户以为声明即生效，而实际上未生效的改动是常见的。
      -->
      <div class="gd-card">
        <div class="gd-card__header">
          <div class="gd-card__headings">
            <div class="gd-card__title">改动汇总</div>
            <div class="gd-card__description">
              模型对原文提出的每一处修改都在这里逐条列出。默认不允许改动；开启「允许模型修正」
              后，每条改动仍需你过目。拒绝即撤回声明，那段文字将保持原文不动。
            </div>
          </div>
          <div class="gd-row">
            <span class="gd-badge" :class="editTotal > 0 ? 'gd-badge--warning' : 'gd-badge--success'">
              {{ editTotal > 0 ? `声明改动 ${editTotal} 处` : '原文将逐字不变' }}
            </span>
          </div>
        </div>

        <div v-if="editTotal > 0" class="gd-row">
          <span v-for="[k, n] in editKindCounts" :key="k" class="gd-badge gd-badge--accent">
            {{ editLabel(k) }} {{ n }}
          </span>
          <span class="gd-grow"></span>
          <button
            type="button"
            class="gd-btn gd-btn--sm gd-btn--success-soft"
            :disabled="gov.verifying"
            @click="acceptAllEdits"
          >
            接受全部
          </button>
          <button
            type="button"
            class="gd-btn gd-btn--sm gd-btn--danger-soft"
            :disabled="gov.verifying"
            @click="rejectAllEdits"
          >
            拒绝全部（还原原文）
          </button>
        </div>

        <!-- 逐条列出：before → after + 类型徽标 + 理由 + 接受/拒绝 -->
        <div v-if="editTotal > 0" class="gd-stack gd-stack--tight">
          <div v-for="item in indexedEditsAll" :key="editKey(item.edit)" class="gd-edit gd-edit--declared">
            <div class="gd-edit__head">
              <span class="gd-badge gd-badge--accent">{{ editLabel(item.edit.kind) }}</span>
              <span class="gd-node__id">{{ item.edit.node_id || '（无节点：插入式改动）' }}</span>
              <span v-if="(item.edit.occurrence ?? 0) > 0" class="gd-muted">
                第 {{ (item.edit.occurrence ?? 0) + 1 }} 处匹配
              </span>
              <!-- before 找不到时这条改动一定不生效，就地告知比等导出后才发现好 -->
              <span
                v-if="item.edit.node_id && !beforeFound(nodeText(item.edit.node_id), item.edit)"
                class="gd-badge gd-badge--warning"
              >
                原文中未找到，不会生效
              </span>
              <span class="gd-grow"></span>
              <button
                type="button"
                class="gd-btn gd-btn--sm gd-btn--success-soft"
                title="确认保留这条改动并重新校验"
                :disabled="gov.verifying"
                @click="acceptEdit(item.edit)"
              >
                接受
              </button>
              <button
                type="button"
                class="gd-btn gd-btn--sm gd-btn--danger-soft"
                title="从方案中删除这条改动，原文将保持不动"
                @click="rejectEdit(item.index, item.edit)"
              >
                拒绝
              </button>
            </div>
            <div class="gd-edit__body">
              <span class="gd-edit__before">{{ item.edit.before || '（空）' }}</span>
              <span class="gd-edit__arrow" aria-hidden="true">→</span>
              <span class="gd-edit__after">{{ item.edit.after || '（删除该内容）' }}</span>
            </div>
            <div v-if="item.edit.reason" class="gd-edit__reason">理由：{{ item.edit.reason }}</div>
          </div>
        </div>

        <div v-else class="gd-alert gd-alert--success">
          <span class="gd-alert__icon" aria-hidden="true">✓</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">没有任何改动</div>
            <div>成品将逐字复制原文，仅调整结构与版式。这正是「原文逐字不变」的默认状态。</div>
          </div>
        </div>

        <!-- 插入式改动（没有 node_id）单独提示，避免用户以为界面漏显示了 -->
        <div v-if="unattachedEdits.length > 0" class="gd-alert gd-alert--info">
          <span class="gd-alert__icon" aria-hidden="true">i</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">另有 {{ unattachedEdits.length }} 处无目标节点的改动</div>
            <div class="gd-alert__list">
              <div v-for="item in unattachedEdits" :key="editKey(item.edit)">
                · {{ editLabel(item.edit.kind) }}：{{ item.edit.after || item.edit.before }}
              </div>
              <div>这类改动（新增标题）由装配器决定落点，因此不绑定到某个原文节点。</div>
            </div>
          </div>
        </div>

        <!-- 成文后的权威结论 -->
        <template v-if="gov.render">
          <div v-if="appliedEdits.length > 0" class="gd-stack gd-stack--tight">
            <div class="gd-field__hint">以下 {{ appliedEdits.length }} 处改动已生效并写入公文文档：</div>
            <div v-for="(a, i) in appliedEdits" :key="`ap-${i}`" class="gd-edit gd-edit--applied">
              <div class="gd-edit__head">
                <span class="gd-badge gd-badge--success">{{ a.label || editLabel(a.kind) }}</span>
                <span class="gd-node__id">{{ a.node_id }}</span>
              </div>
              <div class="gd-edit__body">
                <span class="gd-edit__before">{{ a.before }}</span>
                <span class="gd-edit__arrow" aria-hidden="true">→</span>
                <span class="gd-edit__after">{{ a.after }}</span>
              </div>
              <div v-if="a.reason" class="gd-edit__reason">理由：{{ a.reason }}</div>
            </div>
          </div>

          <div v-if="unappliedEdits.length > 0" class="gd-stack gd-stack--tight">
            <div class="gd-field__error">
              以下 {{ unappliedEdits.length }} 处改动<strong>未能生效</strong> —— 待替换内容在原文中未找到：
            </div>
            <div v-for="(u, i) in unappliedEdits" :key="`un-${i}`" class="gd-edit gd-edit--unapplied">
              <div class="gd-edit__head">
                <span class="gd-badge gd-badge--warning">{{ editLabel(u.kind) }}</span>
                <span class="gd-node__id">{{ u.node_id || '（无节点）' }}</span>
              </div>
              <div class="gd-edit__body">
                <span class="gd-edit__before">{{ u.before }}</span>
              </div>
              <div class="gd-edit__reason">{{ u.reason }}</div>
            </div>
            <div class="gd-field__hint">
              这些改动对文档没有影响（原文完好无损），但说明方案要求的事情做不到。
              可以在上方逐条「拒绝」它们，或核对原文后手工重建一条正确的新增改动。
            </div>
          </div>
        </template>
      </div>

      <!-- 手工新增改动：模型漏掉某处修改时，用户可以自己补一条 -->
      <details class="gd-card gd-card--compact">
        <summary class="gd-disclosure__trigger">
          <span class="gd-disclosure__marker" aria-hidden="true">▸</span>
          手工新增一条改动
        </summary>
        <div class="gd-stack gd-stack--tight" style="margin-top: 0.75rem">
          <div class="gd-field__hint">
            「原文内容」必须与原文<strong>逐字一致</strong>（包含全角空格），它是定位锚点：
            校验要求这段文字确实出现在所选节点里，改动才可能生效。
          </div>

          <div class="gd-field-row">
            <div class="gd-field">
              <label class="gd-label" for="edit-node">原文节点</label>
              <select id="edit-node" v-model="newEdit.nodeId" class="gd-select">
                <option value="">选择节点…</option>
                <option v-for="n in editableNodes" :key="n.id" :value="n.id">
                  {{ n.id }} · {{ n.text.slice(0, 30) }}
                </option>
              </select>
            </div>
            <div class="gd-field">
              <label class="gd-label" for="edit-kind">改动类型</label>
              <select id="edit-kind" v-model="newEdit.kind" class="gd-select">
                <option v-for="k in EDIT_KINDS" :key="k" :value="k">{{ editLabel(k) }}</option>
              </select>
            </div>
          </div>

          <div v-if="newEdit.nodeId !== ''" class="gd-field">
            <span class="gd-label">所选节点的原文</span>
            <div class="gd-node__text" style="font-size: 12px">{{ newEditNodeText || '（无文本）' }}</div>
            <div class="gd-row">
              <button type="button" class="gd-btn gd-btn--sm gd-btn--ghost" @click="fillBeforeFromNode">
                把整段原文填入「原文内容」
              </button>
            </div>
          </div>

          <div class="gd-field">
            <label class="gd-label" for="edit-before">原文内容（before）</label>
            <textarea
              id="edit-before"
              v-model="newEdit.before"
              class="gd-textarea"
              rows="2"
              placeholder="要替换掉的原文片段，必须与原文逐字一致"
            ></textarea>
          </div>

          <div class="gd-field">
            <label class="gd-label" for="edit-after">改为（after）</label>
            <textarea
              id="edit-after"
              v-model="newEdit.after"
              class="gd-textarea"
              rows="2"
              placeholder="替换后的文字；留空表示删除该片段"
            ></textarea>
          </div>

          <div class="gd-field">
            <label class="gd-label" for="edit-reason">
              理由
              <span class="gd-label__optional">可选，但审计时很有用</span>
            </label>
            <input
              id="edit-reason"
              v-model="newEdit.reason"
              class="gd-input"
              type="text"
              placeholder="例如：错别字，「必需」应为「必须」"
              autocomplete="off"
            />
          </div>

          <div class="gd-field">
            <label class="gd-label" for="edit-occurrence">
              第几处匹配
              <span class="gd-label__optional">0 = 第一处</span>
            </label>
            <input
              id="edit-occurrence"
              v-model.number="newEdit.occurrence"
              class="gd-input"
              type="number"
              min="0"
              step="1"
            />
            <div class="gd-field__hint">
              同一段文字在节点里出现多次时，用它指定替换第几处（0 基），与后端
              docmodel.Edit.Occurrence 一致。
            </div>
          </div>

          <div v-if="!newEditBeforeFound" class="gd-alert gd-alert--warning">
            <span class="gd-alert__icon" aria-hidden="true">!</span>
            <div class="gd-alert__body">
              <div class="gd-alert__title">这段文字在所选节点里找不到</div>
              <div>请核对是否有多余的空格或全角/半角差异；提交这样的改动不会生效。</div>
            </div>
          </div>

          <div class="gd-card__footer">
            <button
              type="button"
              class="gd-btn gd-btn--primary"
              :disabled="!newEditValid"
              @click="submitNewEdit"
            >
              新增这条改动
            </button>
          </div>
        </div>
      </details>

      <div class="gd-review">
        <!-- 左：原文节点 -->
        <div class="gd-pane">
          <div class="gd-card gd-card--compact gd-grow">
            <div class="gd-card__header">
              <div class="gd-card__headings">
                <div class="gd-card__title">原文节点</div>
                <div class="gd-card__description">
                  正文 {{ contentTotal }} 段；漏收 {{ nodeCounts.missing }} · 重复 {{ nodeCounts.duplicate }}
                </div>
              </div>
            </div>

            <div class="gd-field">
              <input
                v-model="keyword"
                class="gd-input"
                type="search"
                placeholder="按原文内容或节点号筛选…"
                autocomplete="off"
              />
            </div>

            <div class="gd-filter">
              <button
                v-for="f in ([
                  { k: 'all', label: '全部' },
                  { k: 'missing', label: `漏收 ${nodeCounts.missing}` },
                  { k: 'duplicate', label: `重复 ${nodeCounts.duplicate}` },
                  { k: 'edited', label: `改动 ${nodeCounts.edited}` },
                  { k: 'heading', label: '原文标题' },
                  { k: 'preamble', label: `前言 ${preambleRows.length}` },
                ] as { k: NodeFilter; label: string }[])"
                :key="f.k"
                type="button"
                class="gd-segment__item"
                :aria-pressed="nodeFilter === f.k"
                @click="nodeFilter = f.k"
              >
                {{ f.label }}
              </button>
            </div>

            <div class="gd-pane__scroll gd-stack gd-stack--tight">
              <div
                v-for="row in nodeRows"
                :key="row.node.id"
                class="gd-node"
                :data-kind="row.node.kind"
                :data-status="row.status"
              >
                <div class="gd-node__meta">
                  <span class="gd-node__id">{{ row.node.id }}</span>
                  <span>{{ KIND_LABEL[row.node.kind] }}</span>
                  <span v-if="row.node.source_style">· {{ row.node.source_style }}</span>
                  <!-- 严重问题优先显示；mismatch 必须排在 headingOf 之前，
                       否则「标题不符」会被绿色的「用作标题」徽标盖住。 -->
                  <span v-if="row.status === 'missing'" class="gd-badge gd-badge--danger">漏收</span>
                  <span v-else-if="row.status === 'duplicate'" class="gd-badge gd-badge--warning">
                    重复收录 {{ row.inSections.length }} 次
                  </span>
                  <span v-else-if="row.status === 'mismatch'" class="gd-badge gd-badge--danger">
                    标题与原文不符
                  </span>
                  <span v-else-if="row.elementRole" class="gd-badge gd-badge--accent">
                    要素：{{ ROLE_LABEL[row.elementRole as Role] ?? row.elementRole }}
                  </span>
                  <span v-else-if="preambleHas(gov.plan, row.node.id)" class="gd-badge gd-badge--accent">
                    正文前言
                  </span>
                  <span v-else-if="row.headingOf" class="gd-badge gd-badge--success">
                    用作「{{ row.headingOf.title }}」的标题
                  </span>
                  <span v-else-if="row.inSections.length === 1" class="gd-badge">
                    {{ row.inSections[0].title || row.inSections[0].id }}
                  </span>
                  <span v-if="row.edits.length > 0" class="gd-badge gd-badge--warning">
                    有改动 {{ row.edits.length }} 处
                  </span>
                  <span
                    v-for="k in editKindBadges(row.node.id)"
                    :key="k"
                    class="gd-badge gd-badge--accent"
                  >
                    {{ editLabel(k) }}
                  </span>
                </div>

                <!--
                  就地 diff：有改动时显示「原文（红删除线）→ 改动后（绿新增）」，
                  没有改动时就是逐字原文。任何一种情况都不做 trim ——
                  原文里的全角空格与行尾空格都是内容的一部分。
                -->
                <template v-if="row.rendered">
                  <div class="gd-node__text gd-node__text--before">
                    <span class="gd-node__text-label">原文</span>{{ row.node.text || '（无文本）' }}
                  </div>
                  <div class="gd-node__text gd-node__text--after">
                    <span class="gd-node__text-label">改动后</span>{{ row.rendered.after || '（无文本）' }}
                  </div>
                  <div v-if="row.rendered.failed > 0" class="gd-field__error">
                    有 {{ row.rendered.failed }} 处改动的「原文内容」在该段里找不到，不会生效。
                  </div>
                </template>
                <div v-else class="gd-node__text">{{ row.node.text || '（无文本）' }}</div>

                <!-- 逐条改动的 before → after、理由与接受/拒绝 -->
                <div v-if="row.edits.length > 0" class="gd-stack gd-stack--tight">
                  <div
                    v-for="item in row.edits"
                    :key="editKey(item.edit)"
                    class="gd-edit gd-edit--declared"
                  >
                    <div class="gd-edit__head">
                      <span class="gd-badge gd-badge--accent">{{ editLabel(item.edit.kind) }}</span>
                      <span
                        v-if="!beforeFound(row.node.text, item.edit)"
                        class="gd-badge gd-badge--warning"
                      >
                        原文中未找到
                      </span>
                      <span class="gd-grow"></span>
                      <button
                        type="button"
                        class="gd-btn gd-btn--sm gd-btn--success-soft"
                        :disabled="gov.verifying"
                        @click="acceptEdit(item.edit)"
                      >
                        接受
                      </button>
                      <button
                        type="button"
                        class="gd-btn gd-btn--sm gd-btn--danger-soft"
                        title="删除这条改动，该段保持原文"
                        @click="rejectEdit(item.index, item.edit)"
                      >
                        拒绝
                      </button>
                    </div>
                    <div class="gd-edit__body">
                      <span class="gd-edit__before">{{ item.edit.before }}</span>
                      <span class="gd-edit__arrow" aria-hidden="true">→</span>
                      <span class="gd-edit__after">{{ item.edit.after || '（删除）' }}</span>
                    </div>
                    <div v-if="item.edit.reason" class="gd-edit__reason">理由：{{ item.edit.reason }}</div>
                  </div>
                </div>

                <div v-if="row.issue?.messages?.length" class="gd-node__meta">
                  <span>{{ row.issue.messages[0] }}</span>
                </div>

                <!-- 修复动作：漏收 → 收录到某章节；重复 → 只保留一处 -->
                <div class="gd-node__actions">
                  <template v-if="row.counts && row.status === 'missing'">
                    <select
                      class="gd-select"
                      style="max-width: 13rem; font-size: 12px; padding: 4px 2rem 4px 8px"
                      :value="targetFor(row.node.id)"
                      @change="setCollectTarget(row.node.id, ($event.target as HTMLSelectElement).value)"
                    >
                      <option value="">收录到…</option>
                      <!-- 前言排在第一位：缘由段是最常见的漏收内容，且它本就不该有章节号 -->
                      <option value="__preamble__">正文前言（无编号）</option>
                      <option v-for="o in sectionOptions" :key="o.id" :value="o.id">
                        {{ o.depth > 0 ? '　'.repeat(o.depth) : '' }}{{ o.label }}
                      </option>
                    </select>
                    <button
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--primary"
                      :disabled="targetFor(row.node.id) === ''"
                      @click="targetFor(row.node.id) === '__preamble__' ? addToPreamble(row.node) : collectNode(row.node)"
                    >
                      {{ targetFor(row.node.id) === '__preamble__' ? '移入前言' : '收录' }}
                    </button>
                  </template>

                  <template v-else-if="row.claims.length > 1">
                    <button
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--accent-soft"
                      @click="keepFirstOnly(row.node)"
                    >
                      只保留第一处
                    </button>
                    <button
                      v-for="(site, i) in row.claims"
                      :key="`${site.kind}-${site.sectionId}-${i}`"
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--ghost"
                      :title="`去掉在「${site.label}」的收录`"
                      @click="dropClaim(row.node.id, site)"
                    >
                      移出第 {{ i + 1 }} 处（{{ site.label }}）
                    </button>
                  </template>

                  <template v-else-if="preambleHas(gov.plan, row.node.id)">
                    <button
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--ghost"
                      @click="removeFromPreamble(row.node.id)"
                    >
                      移出正文前言
                    </button>
                  </template>

                  <template v-else-if="row.elementRole">
                    <button
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--ghost"
                      title="解除后该节点可被当作正文收录进章节"
                      @click="unbindElement(row.node.id)"
                    >
                      解除要素绑定
                    </button>
                  </template>

                  <template v-else-if="row.headingOf">
                    <button
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--ghost"
                      @click="detachHeading(row.headingOf.id)"
                    >
                      解除标题关联
                    </button>
                    <button
                      v-if="row.status === 'mismatch'"
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--secondary"
                      @click="restoreHeadingTitle(row.headingOf.id)"
                    >
                      把标题改回原文
                    </button>
                  </template>
                </div>
              </div>

              <div v-if="nodeRows.length === 0" class="gd-empty">
                <div>没有符合条件的节点。</div>
              </div>
            </div>
          </div>
        </div>

        <!-- 右：方案树 -->
        <div class="gd-pane">
          <div class="gd-card gd-card--compact gd-grow">
            <div class="gd-card__header">
              <div class="gd-card__headings">
                <div class="gd-card__title">结构方案</div>
                <div class="gd-card__description">
                  可编辑标题、调整节内顺序、把节点移入其他章节。
                </div>
              </div>
            </div>

            <div class="gd-row">
              <input
                v-model="newTitle"
                class="gd-input gd-grow"
                type="text"
                placeholder="新章节标题（自拟）"
                autocomplete="off"
              />
              <button type="button" class="gd-btn gd-btn--sm gd-btn--secondary" @click="addSection">
                新建章节
              </button>
            </div>

            <div class="gd-pane__scroll gd-tree">
              <!--
                正文前言：主送机关之后、第一个「一、」之前的内容。
                刻意**不**呈现为章节、**不**显示层次序数 —— 它本就没有编号，
                给它编一个「一、」会把第一个真实标题的序数占掉。
              -->
              <div class="gd-section gd-preamble">
                <div class="gd-section__header">
                  <span class="gd-section__level gd-section__level--plain">前言</span>
                  <span class="gd-card__title">正文前言（无编号）</span>
                  <span class="gd-badge">正文 {{ preambleRows.length }} 段</span>
                  <span class="gd-badge gd-badge--accent">不编号 · 非章节</span>
                </div>
                <div class="gd-field__hint">
                  主送机关之后、第一个层次标题之前的内容（如「为贯彻落实……现将有关事项
                  通知如下。」）。它属于正文但不属于任何章节，因此不显示章节号。
                </div>

                <div v-if="preambleRows.length > 0" class="gd-section__body">
                  <div v-for="p in preambleRows" :key="`pre-${p.id}-${p.index}`" class="gd-ref">
                    <span class="gd-ref__index">{{ p.index + 1 }}</span>
                    <span class="gd-node__id">{{ p.id }}</span>
                    <span class="gd-ref__text" :title="p.text">{{ p.text || '（无文本）' }}</span>
                    <span class="gd-ref__actions">
                      <button
                        type="button"
                        class="gd-mini"
                        title="上移"
                        :disabled="p.index === 0"
                        @click="movePreambleNode(p.id, -1)"
                      >
                        ↑
                      </button>
                      <button
                        type="button"
                        class="gd-mini"
                        title="下移"
                        :disabled="p.index === preambleRows.length - 1"
                        @click="movePreambleNode(p.id, 1)"
                      >
                        ↓
                      </button>
                      <button
                        type="button"
                        class="gd-mini gd-mini--danger"
                        title="移出正文前言"
                        @click="removeFromPreamble(p.id)"
                      >
                        ✕
                      </button>
                    </span>
                  </div>
                </div>
                <div v-else class="gd-field__hint">前言目前是空的。</div>

                <!-- 把前言里的节点移入某个章节，或把漏收节点收进前言 -->
                <div v-if="preambleRows.length > 0" class="gd-row">
                  <select
                    class="gd-select"
                    style="max-width: 12rem; font-size: 12px; padding: 4px 2rem 4px 8px"
                    :value="relocateNode['__preamble__'] ?? preambleRows[0].id"
                    @change="pickNode('__preamble__', ($event.target as HTMLSelectElement).value)"
                  >
                    <option v-for="p in preambleRows" :key="p.id" :value="p.id">{{ p.id }}</option>
                  </select>
                  <span class="gd-muted" style="font-size: 12px">移入章节：</span>
                  <select
                    class="gd-select"
                    style="max-width: 14rem; font-size: 12px; padding: 4px 2rem 4px 8px"
                    :value="relocateTo['__preamble__'] ?? ''"
                    @change="pickRelocateTo('__preamble__', ($event.target as HTMLSelectElement).value)"
                  >
                    <option value="">选择章节…</option>
                    <option v-for="o in sectionOptions" :key="o.id" :value="o.id">
                      {{ o.depth > 0 ? '　'.repeat(o.depth) : '' }}{{ o.label }}
                    </option>
                  </select>
                  <button
                    type="button"
                    class="gd-btn gd-btn--sm gd-btn--ghost"
                    :disabled="(relocateTo['__preamble__'] ?? '') === ''"
                    @click="
                      movePreambleTo(relocateNode['__preamble__'] ?? preambleRows[0].id, relocateTo['__preamble__'] ?? '')
                    "
                  >
                    移入
                  </button>
                </div>

                <div class="gd-row">
                  <button
                    type="button"
                    class="gd-btn gd-btn--sm gd-btn--accent-soft"
                    :disabled="nodeCounts.missing === 0"
                    @click="collectAllToPreamble"
                  >
                    把 {{ nodeCounts.missing }} 段漏收收进前言
                  </button>
                </div>
              </div>

              <template v-for="f in flat" :key="f.section.id">
                <div
                  class="gd-section"
                  :class="{ 'gd-section--child': f.depth > 0, 'gd-section--error': sectionHasError(f.section) }"
                >
                  <div class="gd-section__header">
                    <span class="gd-section__level">L{{ f.section.level }}</span>
                    <input
                      class="gd-input gd-section__title-input"
                      :value="f.section.title"
                      :aria-label="`章节 ${f.section.id} 标题`"
                      :data-invalid="f.section.heading_node_id && f.section.title.trim() !== nodeText(f.section.heading_node_id).trim()"
                      @change="rename(f.section.id, ($event.target as HTMLInputElement).value)"
                    />
                    <span class="gd-badge">
                      正文 {{ (f.section.body_node_ids ?? []).length }} 段
                    </span>
                    <span v-if="f.section.heading_node_id" class="gd-badge gd-badge--success">
                      标题取自原文 {{ f.section.heading_node_id }}
                    </span>
                    <span v-else class="gd-badge gd-badge--warning">模型自拟标题</span>
                  </div>

                  <!-- 标题与原文不一致时，给出两条明确的出路 -->
                  <div
                    v-if="
                      f.section.heading_node_id &&
                      f.section.title.trim() !== nodeText(f.section.heading_node_id).trim()
                    "
                    class="gd-alert gd-alert--danger"
                  >
                    <span class="gd-alert__icon" aria-hidden="true">!</span>
                    <div class="gd-alert__body">
                      <div class="gd-alert__title">
                        标题必须与原文逐字一致，否则无法通过校验
                      </div>
                      <div>
                        原文：{{ nodeText(f.section.heading_node_id) }}
                      </div>
                      <div class="gd-row">
                        <button
                          type="button"
                          class="gd-btn gd-btn--sm gd-btn--secondary"
                          @click="restoreHeadingTitle(f.section.id)"
                        >
                          改回原文
                        </button>
                        <button
                          type="button"
                          class="gd-btn gd-btn--sm gd-btn--danger-soft"
                          @click="detachHeading(f.section.id)"
                        >
                          改为自拟标题
                        </button>
                      </div>
                    </div>
                  </div>

                  <div v-if="f.section.rationale" class="gd-field__hint">
                    模型理由：{{ f.section.rationale }}
                  </div>

                  <div v-if="(f.section.body_node_ids ?? []).length > 0" class="gd-section__body">
                    <div
                      v-for="(nid, i) in f.section.body_node_ids ?? []"
                      :key="`${f.section.id}-${nid}-${i}`"
                      class="gd-ref"
                    >
                      <span class="gd-ref__index">{{ i + 1 }}</span>
                      <span class="gd-node__id">{{ nid }}</span>
                      <span class="gd-ref__text" :title="nodeText(nid)">{{ nodeText(nid) || '（无文本）' }}</span>
                      <span class="gd-ref__actions">
                        <button
                          type="button"
                          class="gd-mini"
                          title="上移"
                          :disabled="i === 0"
                          @click="move(f.section.id, nid, -1)"
                        >
                          ↑
                        </button>
                        <button
                          type="button"
                          class="gd-mini"
                          title="下移"
                          :disabled="i === (f.section.body_node_ids ?? []).length - 1"
                          @click="move(f.section.id, nid, 1)"
                        >
                          ↓
                        </button>
                        <button
                          type="button"
                          class="gd-mini gd-mini--danger"
                          title="从此章节移出"
                          @click="detach(f.section.id, nid)"
                        >
                          ✕
                        </button>
                      </span>
                    </div>
                  </div>
                  <div v-else class="gd-field__hint">本节还没有收录任何原文段落。</div>

                  <!-- 移入其他章节 -->
                  <div class="gd-row">
                    <select
                      class="gd-select"
                      style="max-width: 14rem; font-size: 12px; padding: 4px 2rem 4px 8px"
                      :value="relocateTargetOf(f.section.id)"
                      @change="pickRelocateTo(f.section.id, ($event.target as HTMLSelectElement).value)"
                    >
                      <option value="">把本节点移入其他章节…</option>
                      <option v-for="o in sectionOptions.filter((x) => x.id !== f.section.id)" :key="o.id" :value="o.id">
                        {{ o.depth > 0 ? '　'.repeat(o.depth) : '' }}{{ o.label }}
                      </option>
                    </select>
                    <span class="gd-muted" style="font-size: 12px">源节点：</span>
                    <select
                      class="gd-select"
                      style="max-width: 14rem; font-size: 12px; padding: 4px 2rem 4px 8px"
                      :value="movableOf(f.section)"
                      @change="pickNode(f.section.id, ($event.target as HTMLSelectElement).value)"
                    >
                      <option
                        v-for="nid in f.section.body_node_ids ?? []"
                        :key="nid"
                        :value="nid"
                      >
                        {{ nid }}
                      </option>
                    </select>
                    <button
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--ghost"
                      :disabled="
                        (f.section.body_node_ids ?? []).length === 0 ||
                        relocateTargetOf(f.section.id) === ''
                      "
                      @click="relocate(f.section.id, movableOf(f.section), relocateTargetOf(f.section.id))"
                    >
                      移入
                    </button>
                  </div>

                  <!-- 若本节标题是模型自拟的，允许把某段原文标题关联进来 -->
                  <div v-if="!f.section.heading_node_id" class="gd-row">
                    <select
                      class="gd-select"
                      style="max-width: 16rem; font-size: 12px; padding: 4px 2rem 4px 8px"
                      :value="attachNode[f.section.id] ?? ''"
                      @change="pickHeading(f.section.id, ($event.target as HTMLSelectElement).value)"
                    >
                      <option value="">把某段原文标题关联为本节标题…</option>
                      <option
                        v-for="n in sourceHeadings"
                        :key="n.id"
                        :value="n.id"
                      >
                        {{ n.id }} · {{ n.text.slice(0, 24) }}
                      </option>
                    </select>
                    <button
                      type="button"
                      class="gd-btn gd-btn--sm gd-btn--ghost"
                      :disabled="(attachNode[f.section.id] ?? '') === ''"
                      @click="attachHeading(f.section.id, attachNode[f.section.id] ?? '')"
                    >
                      关联并同步标题
                    </button>
                  </div>
                </div>
              </template>

              <div v-if="flat.length === 0" class="gd-empty">
                <div>方案里没有任何章节。</div>
                <div class="gd-muted">可用上方「新建章节」先建一个，再把原文段落收录进来。</div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 成文结果与逐字比对 -->
      <div v-if="gov.render" class="gd-card" :class="{ 'gd-card--secondary': downloadReady }">
        <div class="gd-card__header">
          <div class="gd-card__headings">
            <div class="gd-card__title">成文结果</div>
            <div class="gd-card__description">
              {{ gov.render.filename }} · {{ gov.render.size }} 字节
            </div>
          </div>
          <!-- 显著位置之二：结果卡片上的主按钮 -->
          <div class="gd-row">
            <button
              type="button"
              class="gd-btn gd-btn--secondary"
              :disabled="gov.rendering || !canExport"
              :title="canExport ? '' : '方案有未提交的修改或校验未通过'"
              @click="exportDocx"
            >
              重新生成
            </button>
            <button
              type="button"
              class="gd-btn gd-btn--primary gd-btn--lg"
              :disabled="!downloadReady"
              :title="downloadWhy"
              @click="downloadDocx"
            >
              下载公文文档
            </button>
          </div>
        </div>

        <div v-if="!downloadReady" class="gd-alert gd-alert--warning">
          <span class="gd-alert__icon" aria-hidden="true">!</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">下载已暂停</div>
            <div>{{ downloadWhy }}</div>
          </div>
        </div>

        <!--
          校验通过 + 没有未声明偏差 = 可以直接交付。
          这里刻意用一句话把结论说透，用户不必自己解读三列数字。
        -->
        <div v-else-if="!diffHasProblems" class="gd-alert gd-alert--success">
          <span class="gd-alert__icon" aria-hidden="true">✓</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">逐字比对通过，可以交付</div>
            <div>
              原文没有丢失，也没有任何<strong>未声明</strong>的改写。
              <template v-if="appliedEdits.length > 0">
                其中 {{ appliedEdits.length }} 处改动是模型事先声明、并已在上方「改动汇总」逐条列出的。
              </template>
            </div>
          </div>
        </div>

        <div v-else class="gd-alert gd-alert--danger">
          <span class="gd-alert__icon" aria-hidden="true">!</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">逐字比对发现偏差，请勿直接交付</div>
            <div>下面标红的条目意味着原文丢失或被未声明地改写了，需要回到校对区处理。</div>
          </div>
        </div>

        <div v-if="gov.render.diff" class="gd-diff">
          <div class="gd-diff__col" :class="(gov.render.diff.missing?.length ?? 0) > 0 ? 'gd-diff__col--missing' : ''">
            <div class="gd-diff__col-title">缺失（必须为 0）: {{ gov.render.diff.missing?.length ?? 0 }}</div>
            <div v-for="(m, i) in gov.render.diff.missing ?? []" :key="i" class="gd-diff__item">{{ m }}</div>
            <div v-if="(gov.render.diff.missing?.length ?? 0) === 0" class="gd-muted">原文无缺失。</div>
          </div>
          <div class="gd-diff__col" :class="(gov.render.diff.altered?.length ?? 0) > 0 ? 'gd-diff__col--altered' : ''">
            <div class="gd-diff__col-title">被改写（必须为 0）: {{ gov.render.diff.altered?.length ?? 0 }}</div>
            <div v-for="(m, i) in gov.render.diff.altered ?? []" :key="i" class="gd-diff__item">{{ m }}</div>
            <div v-if="(gov.render.diff.altered?.length ?? 0) === 0" class="gd-muted">没有任何原文被未声明地改写。</div>
          </div>
          <div class="gd-diff__col gd-diff__col--extra">
            <div class="gd-diff__col-title">多出（应为装配器生成的脚手架）: {{ gov.render.diff.extra?.length ?? 0 }}</div>
            <div v-for="(m, i) in gov.render.diff.extra ?? []" :key="i" class="gd-diff__item">{{ m }}</div>
            <div v-if="(gov.render.diff.extra?.length ?? 0) === 0" class="gd-muted">没有额外文本。</div>
          </div>
        </div>

        <div class="gd-row">
          <span class="gd-badge" :class="appliedEdits.length > 0 ? 'gd-badge--success' : ''">
            已生效改动 {{ appliedEdits.length }} 处
          </span>
          <span class="gd-badge" :class="unappliedEdits.length > 0 ? 'gd-badge--warning' : ''">
            未生效改动 {{ unappliedEdits.length }} 处
          </span>
          <span class="gd-muted" style="font-size: 12px">逐条清单见上方「改动汇总」面板。</span>
        </div>

        <div v-if="gov.render.synth_text?.length" class="gd-field__hint">
          装配器生成的文本（层次序数、版头等）：{{ gov.render.synth_text.join('｜') }}
        </div>

        <div v-if="gov.plan?.notes?.length" class="gd-alert gd-alert--info">
          <span class="gd-alert__icon" aria-hidden="true">i</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">模型的说明</div>
            <div v-for="(n, i) in gov.plan.notes" :key="i" class="gd-alert__list">{{ n }}</div>
          </div>
        </div>
      </div>

      <div class="gd-row gd-row--end">
        <button type="button" class="gd-btn gd-btn--secondary gd-btn--lg" @click="emit('preview')">
          查看分页预览
        </button>
      </div>
    </template>
  </section>
</template>
