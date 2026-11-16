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
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Disclosure, Tabs, ToggleButton, ToggleButtonGroup } from '@heroui/react'
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
import {
  blockReasons,
  issuesByNode,
  nodeStatus,
  type NodeIssue,
  type NodeStatus,
} from '../lib/report'
import {
  downloadRenderedFile,
  editPlan,
  gov,
  loadSource,
  notify,
  renderDocument,
  verifyPlanNow,
} from '../store/govdoc'
import { useGov } from '../store/useGov'
import PreviewPane from './PreviewPane'
import {
  AlertBox,
  Badge,
  Btn,
  EmptyState,
  Field,
  InlineSpinner,
  Row,
  SectionCard,
  Select,
  Stack,
  TextArea,
  TextInput,
  type SelectItem,
} from '../ui/primitives'

type NodeFilter = 'all' | 'missing' | 'duplicate' | 'heading' | 'preamble' | 'edited'

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

interface NodeRow {
  node: NodeSummary
  status: NodeStatus
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
   * 在 useMemo 里算一次，而不是在模板里反复调用函数：模板每次重渲染都会
   * 重新求值，大文档下会平白多出成倍的字符串拼接。
   */
  rendered: NodeRender | null
}

interface NewEditForm {
  nodeId: string
  kind: EditKind
  before: string
  after: string
  reason: string
  occurrence: number
}

const CARD_ROW = 'flex flex-wrap items-center justify-between gap-2'
const EDIT_BLOCK = 'space-y-1.5 rounded-lg border border-border bg-surface-secondary p-2.5'
const EDIT_TEXT = 'text-sm'
const HINT = 'text-xs text-muted'

/**
 * 该节点的文本按改动重演之后的结果；无改动时返回 null。
 *
 * 这是唯一一处「本地预览成品」的地方，与服务端的 AppliedText 互为镜像。
 * 若二者有出入，以服务端为准 —— 导出结果里的 diff.changed / diff.unapplied
 * 才是权威结论。
 */
function renderedOf(items: IndexedEdit[], nodeText: string): NodeRender | null {
  if (items.length === 0) return null
  const { text, failed } = applyEdits(
    nodeText,
    items.map((i) => i.edit),
  )
  const changed = text !== nodeText
  return { before: nodeText, after: text, failed: failed.length, changed }
}

/** 截断长文本用于提示。 */
function clip(s: string, n = 24): string {
  return s.length <= n ? s : `${s.slice(0, n)}…`
}

/** 结构变动的中文标签。 */
const MOVE_LABEL: Record<string, string> = {
  moved: '换了章节',
  to_preamble: '移入正文前言',
  from_preamble: '从前言移入章节',
  to_element: '改作公文要素',
}

const SECTION_CHANGE_LABEL: Record<string, string> = {
  added: '新增章节',
  missing: '原文有、方案没有',
  retitled: '标题被改写',
}

/**
 * 结构变动：方案与「原文自身结构」的差异。
 *
 * 基线是不看模型、直接按原文自带的层次序数切出来的方案，所以这份差异回答的是
 * "模型把哪一段挪到了别处、把哪个章节改了名或换了位置"。没有它，用户只能靠
 * 通读原稿与成品才能发现归属错了 —— 而归属错正是本工具最可能的失败方式。
 *
 * 差异为空时不显示任何东西：正常情况就应该什么都不显示。
 */
function StructurePanel() {
  const diff = gov.structure
  if (!diff || diffEmpty(diff)) return null

  const moves = diff.moves ?? []
  const changes = diff.sections ?? []
  const moved = diff.moved_sections ?? []

  return (
    <SectionCard
      compact
      title="结构变动"
      actions={
        <Badge color="warning">
          移动 {moves.length} 段 · 章节 {changes.length + moved.length} 处
        </Badge>
      }
    >
      {moves.length > 0 && (
        <div className="space-y-1">
          {moves.map((m) => (
            <div
              key={`mv-${m.node_id}-${m.to_id}`}
              className="rounded-md bg-surface-secondary p-2 text-[13px] leading-relaxed"
            >
              <Row className="text-xs">
                <Badge color="accent">{MOVE_LABEL[m.kind] ?? m.kind}</Badge>
                <span className="font-mono text-muted">{m.node_id}</span>
              </Row>
              <div className="text-muted line-clamp-2">{m.text}</div>
              <div className="text-xs">
                <span className="text-muted">{m.from}</span>
                <span className="mx-1">→</span>
                <strong>{m.to}</strong>
              </div>
            </div>
          ))}
        </div>
      )}

      {changes.length > 0 && (
        <div className="space-y-1">
          {changes.map((c) => (
            <div
              key={`sc-${c.kind}-${c.id}`}
              className="rounded-md bg-surface-secondary p-2 text-[13px] leading-relaxed"
            >
              <Row className="text-xs">
                <Badge color={c.kind === 'missing' ? 'danger' : 'warning'}>
                  {SECTION_CHANGE_LABEL[c.kind] ?? c.kind}
                </Badge>
                <span className="font-mono text-muted">{c.id}</span>
              </Row>
              <div>
                {c.kind === 'retitled' ? (
                  <>
                    <span className="text-muted line-through">{c.baseline_title}</span>
                    <span className="mx-1">→</span>
                    <strong>{c.title}</strong>
                  </>
                ) : (
                  <strong>{c.title}</strong>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {moved.length > 0 && (
        <div className="space-y-1">
          {moved.map((s) => (
            <div
              key={`sm-${s.id}`}
              className="rounded-md bg-surface-secondary p-2 text-[13px] leading-relaxed"
            >
              <Row className="text-xs">
                <Badge color="warning">换了位置</Badge>
                <span className="font-mono text-muted">{s.id}</span>
                <span className="ms-auto text-muted">
                  原文第 {s.baseline_pos} 章 → 成品第 {s.rendered_pos} 章
                </span>
              </Row>
              <div>{s.title}</div>
            </div>
          ))}
        </div>
      )}
    </SectionCard>
  )
}

/** 结构差异是否为空（服务端可能返回空数组而非省略字段）。 */
function diffEmpty(d: {
  moves?: unknown[]
  sections?: unknown[]
  moved_sections?: unknown[]
}): boolean {
  return (
    (d.moves?.length ?? 0) === 0 &&
    (d.sections?.length ?? 0) === 0 &&
    (d.moved_sections?.length ?? 0) === 0
  )
}

export default function PanelReview({ onPreview }: { onPreview: () => void }) {
  useGov()

  const docId = useMemo(() => gov.upload?.doc_id ?? '', [gov.upload])

  useEffect(() => {
    const id = gov.upload?.doc_id ?? ''
    if (id !== '') void loadSource(id)
  }, [])

  // ---------------------------------------------------------------- 原文左栏

  const [nodeFilter, setNodeFilter] = useState<NodeFilter>('all')
  const [keyword, setKeyword] = useState('')

  const issues = useMemo(() => issuesByNode(gov.report), [gov.report])

  /**
   * 原文节点索引：id → 节点。
   *
   * 方案树里每一处引用都要显示原文文本，逐条 array.find 会让大的方案变成
   * O(n·m)；建一次索引把渲染压回线性。
   */
  const nodeIndex = useMemo(() => {
    const map = new Map<string, NodeSummary>()
    for (const n of gov.source?.doc.nodes ?? gov.upload?.nodes ?? []) map.set(n.id, n)
    return map
  }, [gov.source, gov.upload])

  const nodeText = (id: string): string => nodeIndex.get(id)?.text ?? ''

  /**
   * 章节索引：section id → 章节。
   *
   * headingNodeOwner / sectionsContainingNode 是 O(树)，在 v-for 里逐行调用
   * 会重复遍历整棵树；这里一次遍历建表。
   */
  const sectionIndex = useMemo(() => {
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
  }, [gov.plan])

  // ---------------------------------------------------------------- 改动索引

  /**
   * 全部改动，带在 plan.edits 里的下标。
   *
   * 下标是「拒绝」的依据：服务端契约就是按下标删除该条，因此必须在整张表上
   * 保留原始下标，而不是在某节点过滤后的子表里另算。
   */
  const indexedEditsAll = useMemo<IndexedEdit[]>(() => {
    const out: IndexedEdit[] = []
    ;(gov.plan?.edits ?? []).forEach((edit, index) => out.push({ edit, index }))
    return out
  }, [gov.plan])

  /**
   * 一个节点上挂的改动（带在 plan.edits 里的下标）。
   *
   * 用 O(1) 索引而不是在每个节点里逐条扫描 plan.edits：大文档的节点数可能上千，
   * 每个节点都遍历一遍改动表会让渲染退化成平方复杂度。
   */
  const editsByNode = useMemo(() => {
    const map = new Map<string, IndexedEdit[]>()
    for (const item of indexedEditsAll) {
      const id = item.edit.node_id ?? ''
      if (id === '') continue
      const list = map.get(id)
      if (list) list.push(item)
      else map.set(id, [item])
    }
    return map
  }, [indexedEditsAll])

  /** 该节点带的改动类型徽标（去重，按声明顺序）。 */
  function editKindBadges(nodeId: string): string[] {
    const seen = new Set<string>()
    for (const { edit } of editsByNode.get(nodeId) ?? []) seen.add(edit.kind)
    return [...seen]
  }

  const nodeRows = useMemo<NodeRow[]>(() => {
    const nodes = gov.source?.doc.nodes ?? gov.upload?.nodes ?? []
    const kw = keyword.trim()
    const plan = gov.plan

    return nodes
      .filter((n) => {
        const status = nodeStatus(issues.get(n.id))
        switch (nodeFilter) {
          case 'missing':
            return status === 'missing'
          case 'duplicate':
            return status === 'duplicate'
          case 'heading':
            return sectionIndex.byHeading.has(n.id)
          case 'preamble':
            return preambleHas(plan, n.id)
          case 'edited':
            // 「有改动」按 node_id 命中：这样筛选出来的是「模型想动哪些段」，
            // 正是用户复核改动时最需要的那一屏。
            return editsByNode.has(n.id)
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
          (editsByNode.get(n.id) ?? []).some(
            (e) =>
              e.edit.before.includes(kw) ||
              e.edit.after.includes(kw) ||
              (e.edit.reason ?? '').includes(kw),
          ),
      )
      .map((n) => {
        const nodeEdits = editsByNode.get(n.id) ?? []
        return {
          node: n,
          status: nodeStatus(issues.get(n.id)),
          issue: issues.get(n.id),
          inSections: sectionIndex.bodyOf.get(n.id) ?? [],
          headingOf: sectionIndex.byHeading.get(n.id) ?? null,
          elementRole: elementRoleOf(plan, n.id),
          claims: plan ? claimSites(plan, n.id) : [],
          counts: n.kind !== 'page_break',
          edits: nodeEdits,
          rendered: renderedOf(nodeEdits, n.text),
        }
      })
  }, [gov.source, gov.upload, gov.plan, nodeFilter, keyword, issues, sectionIndex, editsByNode])

  /** 左栏筛选按钮上的计数。 */
  const nodeCounts = useMemo(() => {
    let missing = 0
    let duplicate = 0
    for (const [, issue] of issues) {
      const st = nodeStatus(issue)
      if (st === 'missing') missing++
      if (st === 'duplicate') duplicate++
    }
    // 带改动的节点数（一个节点可能有多条改动）。
    let edited = 0
    for (const n of gov.source?.doc.nodes ?? gov.upload?.nodes ?? []) {
      if (editsByNode.has(n.id)) edited++
    }
    return { missing, duplicate, edited }
  }, [issues, editsByNode, gov.source, gov.upload])

  // ---------------------------------------------------------------- 改动汇总

  /** 方案里声明的改动总数。 */
  const editTotal = indexedEditsAll.length

  /** 没有任何节点指向的改动（EditHeading 插入式），单独列出以免用户看不到。 */
  const unattachedEdits = useMemo(() => orphanEdits(gov.plan), [gov.plan])

  /** 改动汇总：按类型计数，用于顶部概览徽标。 */
  const editKindCounts = useMemo(() => {
    const map = new Map<string, number>()
    for (const { edit } of indexedEditsAll) map.set(edit.kind, (map.get(edit.kind) ?? 0) + 1)
    return [...map.entries()]
  }, [indexedEditsAll])

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
    const n = editTotal
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
    if (editTotal === 0) {
      notify('info', '当前没有任何改动。')
      return
    }
    const ok = await verifyPlanNow()
    notify(
      ok ? 'ok' : 'info',
      ok
        ? `已接受全部 ${editTotal} 处改动，方案校验通过。`
        : `已提交全部 ${editTotal} 处改动，但方案仍未通过校验，请看下方原因。`,
    )
  }

  // ------------------------------------------------- 手工新增改动

  /**
   * 新增改动表单。
   *
   * node_id 与 before 是关键：before 必须**逐字**取自原文（含全角空格），所以
   * 表单里提供一个「用该节点原文填入」的按钮，并允许粘贴。手动敲错一个字，
   * 这条改动就会因为 before 找不到而不生效 —— 服务端会在 diff.unapplied 里点出来。
   */
  const [newEdit, setNewEdit] = useState<NewEditForm>({
    nodeId: '',
    kind: 'typo',
    before: '',
    after: '',
    reason: '',
    occurrence: 0,
  })

  function patchNewEdit(patch: Partial<NewEditForm>): void {
    setNewEdit((cur) => ({ ...cur, ...patch }))
  }

  /** 可被改动的节点：携带正文、且有文本的节点。 */
  const editableNodes = useMemo(
    () =>
      (gov.source?.doc.nodes ?? gov.upload?.nodes ?? []).filter(
        (n) => n.kind !== 'page_break' && (n.text ?? '') !== '',
      ),
    [gov.source, gov.upload],
  )

  /** 新增表单里选中节点的原文，用于「填入原文」与实时校验 before。 */
  const newEditNodeText = useMemo(() => nodeText(newEdit.nodeId), [newEdit.nodeId, nodeIndex])

  /** before 是否能在所选节点里找到 —— 找不到这条改动一定不生效，提前告知。 */
  const newEditBeforeFound = useMemo(() => {
    if (newEdit.nodeId === '' || newEdit.before === '') return true
    return beforeFound(newEditNodeText, {
      kind: newEdit.kind,
      before: newEdit.before,
      after: newEdit.after,
    })
  }, [newEdit.nodeId, newEdit.before, newEdit.kind, newEdit.after, newEditNodeText])

  /** 表单是否可以提交。 */
  const newEditValid = useMemo(
    () =>
      newEdit.nodeId !== '' &&
      newEdit.before !== '' &&
      newEdit.before !== newEdit.after &&
      newEditBeforeFound,
    [newEdit.nodeId, newEdit.before, newEdit.after, newEditBeforeFound],
  )

  /** 把所选节点的原文填进 before（逐字，不做任何修剪）。 */
  function fillBeforeFromNode(): void {
    patchNewEdit({ before: newEditNodeText })
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
    if (!newEditBeforeFound) {
      notify(
        'error',
        '「原文内容」在所选节点里找不到，这条改动不会生效。请核对（注意全角空格也要一致）。',
      )
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
    patchNewEdit({ before: '', after: '', reason: '', occurrence: 0 })
  }

  // ------------------------------------------------- 成文结果的改动清单

  /** 成文后已生效的改动（服务端权威结论）。 */
  const appliedEdits = useMemo(() => gov.render?.diff?.changed ?? [], [gov.render])

  /** 成文后未能生效的改动。 */
  const unappliedEdits = useMemo(() => gov.render?.diff?.unapplied ?? [], [gov.render])

  /**
   * 逐字比对的三类条目（missing / altered / extra）。
   *
   * 三者都是**字符串**，不是带 node_id 的对象 —— 服务端把缺失、改写、多出的
   * 内容都格式化成一行可读说明（见 internal/docmodel/verify.go 的 describeNode）。
   * 所以这里直接按文本渲染，不要试图去读 .node_id / .text。
   */
  const missingTexts = useMemo(() => gov.render?.diff?.missing ?? [], [gov.render])
  const alteredTexts = useMemo(() => gov.render?.diff?.altered ?? [], [gov.render])
  const extraTexts = useMemo(() => gov.render?.diff?.extra ?? [], [gov.render])

  /** 成文后的逐字比对是否存在真实偏差（missing / altered）。 */
  const diffHasProblems = useMemo(() => {
    const d = gov.render?.diff
    if (!d) return false
    return (d.missing?.length ?? 0) > 0 || (d.altered?.length ?? 0) > 0
  }, [gov.render])

  // ---------------------------------------------------------------- 方案右栏

  const flat = useMemo(() => flattenSections(gov.plan?.sections ?? []), [gov.plan])

  /** 所有可作为「移入目标」的章节。 */
  const sectionOptions = useMemo(
    () => flat.map((f) => ({ id: f.section.id, label: f.label, depth: f.depth })),
    [flat],
  )

  /** 每个漏收节点的「收录到哪个章节」选择（键：node id → section id）。 */
  const [collectTarget, setCollectTarget] = useState<Record<string, string>>({})

  /** 每个章节的「把哪个节点移入」选择（键：章节 id → 被移动的节点 id）。 */
  const [relocateNode, setRelocateNode] = useState<Record<string, string>>({})

  /** 每个章节的「移入到哪个章节」选择（键：章节 id → 目标 section id）。 */
  const [relocateTo, setRelocateTo] = useState<Record<string, string>>({})

  /** 每个章节的「关联哪段原文标题」选择（键：章节 id → node id）。 */
  const [attachNode, setAttachNode] = useState<Record<string, string>>({})

  function targetFor(nodeId: string): string {
    return collectTarget[nodeId] ?? ''
  }

  function setCollectTargetFor(nodeId: string, sectionId: string): void {
    setCollectTarget((cur) => ({ ...cur, [nodeId]: sectionId }))
  }

  function pickNode(sectionId: string, nodeId: string): void {
    setRelocateNode((cur) => ({ ...cur, [sectionId]: nodeId }))
  }

  function pickRelocateTo(sectionId: string, targetId: string): void {
    setRelocateTo((cur) => ({ ...cur, [sectionId]: targetId }))
  }

  function pickHeading(sectionId: string, nodeId: string): void {
    setAttachNode((cur) => ({ ...cur, [sectionId]: nodeId }))
  }

  /** 章节内当前选中的待移出节点（默认第一个）。 */
  function movableOf(section: Section): string {
    return relocateNode[section.id] ?? section.body_node_ids?.[0] ?? ''
  }

  /** 章节当前选中的移入目标。 */
  function relocateTargetOf(sectionId: string): string {
    return relocateTo[sectionId] ?? ''
  }

  /** 原文顺序表：id → Order。按原文顺序插入时用它决定插入位置。 */
  const sourceOrder = useMemo(() => {
    const map = new Map<string, number>()
    for (const n of gov.source?.doc.nodes ?? gov.upload?.nodes ?? []) map.set(n.id, n.order)
    return map
  }, [gov.source, gov.upload])

  /** 把漏收节点插入所选章节，插入位置按原文顺序（避免平白触发 reordered 警告）。 */
  function collectNode(node: NodeSummary): void {
    const target = targetFor(node.id)
    if (target === '') {
      notify('error', '请先选择要把这一段收录到哪个章节。')
      return
    }
    editPlan((plan) => placeBodyNodeInSourceOrder(plan, target, node.id, sourceOrder))
    notify('ok', `已把 ${node.id} 按其原文位置收录到所选章节，正在重新校验。`)
  }

  /** 一次性把当前所有漏收的正文节点收进所选章节 —— 批量修复漏收。 */
  function collectAllMissing(target: string): void {
    if (target === '') {
      notify('error', '请先选择要批量收录到哪个章节。')
      return
    }
    const missing = [...issues.entries()]
      .filter(([, issue]) => nodeStatus(issue) === 'missing')
      .map(([id]) => id)
      .sort((a, b) => (sourceOrder.get(a) ?? 0) - (sourceOrder.get(b) ?? 0))
    if (missing.length === 0) {
      notify('info', '当前没有漏收的正文节点。')
      return
    }
    editPlan((plan) => {
      let next = plan
      for (const id of missing) {
        next = placeBodyNodeInSourceOrder(next, target, id, sourceOrder)
      }
      return next
    })
    notify('ok', `已把 ${missing.length} 段漏收原文按其原文顺序收进所选章节，正在重新校验。`)
  }

  // ---------------------------------------------------------------- 正文前言

  /** 正文前言里的节点，按前言顺序。 */
  const preambleRows = useMemo(() => {
    const ids = gov.plan?.preamble_node_ids ?? []
    return ids.map((id, i) => ({ id, index: i, text: nodeText(id) }))
  }, [gov.plan, nodeIndex])

  /** 把节点加入正文前言（移出它在别处的认领，故不会产生重复）。 */
  function addToPreamble(node: NodeSummary): void {
    editPlan((plan) => attachPreamble(plan, node.id, sourceOrder))
    notify(
      'ok',
      `已把 ${node.id} 移入「正文前言（无编号）」；前言不会编号，也不是章节。正在重新校验。`,
    )
  }

  /** 从前言移出一个节点到指定章节。 */
  function movePreambleTo(nodeId: string, sectionId: string): void {
    if (sectionId === '') {
      notify('error', '请先选择要移入哪个章节。')
      return
    }
    editPlan((plan) => placeBodyNodeInSourceOrder(plan, sectionId, nodeId, sourceOrder))
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
    const missing = [...issues.entries()]
      .filter(([, issue]) => nodeStatus(issue) === 'missing')
      .map(([id]) => id)
      .sort((a, b) => (sourceOrder.get(a) ?? 0) - (sourceOrder.get(b) ?? 0))
    if (missing.length === 0) {
      notify('info', '当前没有漏收的正文节点。')
      return
    }
    editPlan((plan) => {
      let next = plan
      for (const id of missing) next = attachPreamble(next, id, sourceOrder)
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
      notify(
        'ok',
        `已把 ${node.id} 的 ${sites.length} 处认领合并保留为「${sites[0].label}」，正在重新校验。`,
      )
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
    editPlan((plan) => placeBodyNodeInSourceOrder(plan, toSectionId, nodeId, sourceOrder))
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
    editPlan((plan) => ({
      ...plan,
      sections: attachSectionHeading(plan.sections, sectionId, nodeId, text),
    }))
  }

  /** 把章节标题改回原文标题节点的文本，一键修好 heading_mismatch。 */
  function restoreHeadingTitle(sectionId: string): void {
    const sec = flat.find((f) => f.section.id === sectionId)?.section
    if (!sec?.heading_node_id) return
    const text = nodeText(sec.heading_node_id)
    editPlan((plan) => ({ ...plan, sections: setSectionTitle(plan.sections, sectionId, text) }))
    notify('ok', '标题已恢复为原文文本。')
  }

  /** 新建章节：把尚未归属的段落有个去处。 */
  const [newTitle, setNewTitle] = useState('')

  function addSection(): void {
    const title = newTitle.trim()
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
    setNewTitle('')
    notify(
      'ok',
      `已新建章节「${title}」（${createdId}），它是自拟标题，可继续把原文段落收录进来。`,
    )
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
      (d) =>
        d.severity === 'error' &&
        (d.code === 'bad_level' || d.node_ids?.some((i) => ids.has(i) || i === s.id)),
    )
  }

  // ---------------------------------------------------------------- 校验与导出

  const reasons = useMemo(() => blockReasons(gov.report), [gov.report])
  const canExport = useMemo(
    () => gov.plan !== null && !gov.dirty && reasons.length === 0 && !gov.rendering,
    [gov.plan, gov.dirty, reasons, gov.rendering],
  )

  /**
   * 「下载公文文档」的可用性。
   *
   * 与 canExport 不同：canExport 要求本地没有未提交的改动，而下载用的是**已经
   * 生成好的文件**，所以本地是否 dirty 不影响它能不能下 —— 只要求那份文件仍与
   * 当前校验结论一致（report 通过）。否则用户会把一份与屏幕上方案已不符的旧文件
   * 当成合规交付物拿走。
   */
  const downloadReady = useMemo(
    () => gov.render !== null && reasons.length === 0,
    [gov.render, reasons],
  )

  /** 下载按钮不可用时的中文原因。 */
  const downloadWhy = useMemo(() => {
    if (gov.render === null) return '还没有生成公文文档，请先点「生成公文文档」。'
    if (reasons.length > 0) return '当前方案未通过校验，已暂停下载。请先修好下方问题。'
    return ''
  }, [gov.render, reasons])

  /** 是否已经下载过这份文件（按钮保持可用，仅作提示）。 */
  const alreadyDownloaded = useMemo(
    () => gov.render !== null && gov.downloaded.includes(gov.render.file_id),
    [gov.render, gov.downloaded],
  )

  async function revalidate(): Promise<void> {
    await verifyPlanNow()
  }

  async function exportDocx(): Promise<void> {
    await renderDocument(docId)
  }

  /** 「下载公文文档」主按钮：直接触发 GET /api/files/{file_id}。 */
  function downloadDocx(): void {
    downloadRenderedFile()
  }

  const refCount = useMemo(() => countBodyRefs(gov.plan?.sections ?? []), [gov.plan])

  /** 可作为章节标题关联对象的原文标题节点。 */
  const sourceHeadings = useMemo(
    () => (gov.source?.doc.nodes ?? gov.upload?.nodes ?? []).filter((n) => n.kind === 'heading'),
    [gov.source, gov.upload],
  )

  /** 批量收录漏收节点时的目标章节；`__preamble__` 表示正文前言。 */
  const [bulkTarget, setBulkTarget] = useState('')

  function bulkCollect(): void {
    if (bulkTarget === '__preamble__') {
      collectAllToPreamble()
      return
    }
    collectAllMissing(bulkTarget)
  }

  /** 批量把多处认领的节点各合并为一处（修复模型重复收录）。 */
  function dedupeAll(): void {
    // 候选来自两处：界面已算出的 claims（覆盖「要素绑定 + 章节正文」这类
    // 报告未必点名的组合）与报告点名的 duplicate_content 节点。
    const candidates = new Set<string>()
    for (const row of nodeRows) {
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

  if (!gov.plan) {
    return (
      <section className="space-y-4">
        <header className="space-y-1">
          <h2 className="text-xl font-semibold">校对与输出</h2>
        </header>
        <SectionCard>
          <EmptyState>
            <div>还没有结构方案。</div>
          </EmptyState>
        </SectionCard>
      </section>
    )
  }

  const plan = gov.plan
  const render = gov.render

  /** 节点状态徽标：严重问题优先显示；mismatch 必须排在 headingOf 之前。 */
  function nodeStatusBadge(row: NodeRow): ReactNode {
    if (row.status === 'missing') return <Badge color="danger">漏收</Badge>
    if (row.status === 'duplicate') {
      return <Badge color="warning">重复收录 {row.inSections.length} 次</Badge>
    }
    if (row.status === 'mismatch') return <Badge color="danger">标题与原文不符</Badge>
    if (row.elementRole) {
      return (
        <Badge color="accent">
          要素：{ROLE_LABEL[row.elementRole as Role] ?? row.elementRole}
        </Badge>
      )
    }
    if (preambleHas(plan, row.node.id)) return <Badge color="accent">正文前言</Badge>
    if (row.headingOf) {
      return <Badge color="success">用作「{row.headingOf.title}」的标题</Badge>
    }
    if (row.inSections.length === 1) {
      return <Badge>{row.inSections[0].title || row.inSections[0].id}</Badge>
    }
    return null
  }

  /**
   * 章节下拉选项（按层次深度缩进；excludeId 用于「移入其他章节」）。
   *
   * 缩进用全角空格而不是空格：HeroUI 的下拉项不会保留连续半角空格的宽度，
   * 全角空格既能对齐又能看出层级。
   */
  function sectionItems(excludeId?: string): SelectItem[] {
    return sectionOptions
      .filter((o) => o.id !== excludeId)
      .map((o) => ({
        value: o.id,
        label: `${o.depth > 0 ? '　'.repeat(o.depth) : ''}${o.label}`,
      }))
  }

  return (
    <section className="space-y-4">
      <header className="space-y-1">
        <h2 className="text-xl font-semibold">校对与输出</h2>
      </header>

      {render && (
        <SectionCard compact className="bg-surface-secondary">
          <div className={CARD_ROW}>
            <Row>
              <Badge color={downloadReady ? 'success' : 'danger'}>
                {downloadReady ? '公文文档已生成' : '文件已生成但方案已变更'}
              </Badge>
              <span className={HINT}>{render.filename}</span>
              {alreadyDownloaded && <Badge>已下载过</Badge>}
            </Row>
            <Row>
              <Btn
                size="sm"
                variant="ghost"
                isDisabled={gov.rendering || !canExport}
                title={canExport ? '' : '方案有未提交的修改或校验未通过'}
                onPress={() => void exportDocx()}
              >
                重新生成
              </Btn>
              <Btn
                variant="primary"
                isDisabled={!downloadReady}
                title={downloadWhy}
                onPress={downloadDocx}
              >
                下载公文文档
              </Btn>
            </Row>
          </div>
          {!downloadReady && downloadWhy !== '' && <div className={HINT}>{downloadWhy}</div>}
        </SectionCard>
      )}

      {/* 校验状态条 */}
      <SectionCard compact>
        <div className={CARD_ROW}>
          <Row>
            <Badge color={reasons.length === 0 ? 'success' : 'danger'}>
              {reasons.length === 0 ? '校验通过' : '校验未通过'}
            </Badge>
            <span className={HINT}>{reportSummary(gov.report)}</span>
            {gov.dirty ? (
              <Badge color="warning">有未提交的修改</Badge>
            ) : gov.verifying ? (
              <Badge color="accent">
                <span className="inline-flex items-center gap-1">
                  <InlineSpinner className="size-3" /> 校验中
                </span>
              </Badge>
            ) : null}
          </Row>
          <Row>
            <span className={HINT}>
              章节 {flat.length} 个 · 正文引用 {refCount} 处
              {(plan.preamble_node_ids ?? []).length > 0 &&
                ` · 前言 ${(plan.preamble_node_ids ?? []).length} 段`}
              {(gov.report?.bound_elements ?? 0) > 0 && ` · 公文要素 ${gov.report?.bound_elements}`}
              {editTotal > 0 && ` · 改动 ${editTotal} 处`}
            </span>
            <Btn size="sm" variant="ghost" isDisabled={gov.verifying} onPress={() => void revalidate()}>
              重新校验
            </Btn>
            <Btn
              size="sm"
              variant="secondary"
              isDisabled={!canExport}
              title={reasons.length > 0 ? '校验未通过，无法成文' : ''}
              onPress={() => void exportDocx()}
            >
              {gov.rendering && <InlineSpinner className="mr-1 size-3" />}
              {gov.rendering ? '成文中…' : render ? '重新生成公文文档' : '生成公文文档'}
            </Btn>
            {render && (
              <Btn
                size="sm"
                variant="primary"
                isDisabled={!downloadReady}
                title={downloadWhy}
                onPress={downloadDocx}
              >
                下载公文文档
              </Btn>
            )}
          </Row>
        </div>

        {reasons.length > 0 && (
          <AlertBox status="danger" title="导出已禁用，请先修复以下问题">
            {reasons.map((r) => (
              <div key={r}>· {r}</div>
            ))}
          </AlertBox>
        )}

        {/* 批量修复：模型漏收/重复一整批时，逐条点太慢 */}
        {(nodeCounts.missing > 0 || nodeCounts.duplicate > 0) && (
          <Row>
            <span className={HINT}>批量修复：</span>
            <Select
              compact
              className="w-auto min-w-56 max-w-64"
              aria-label="批量修复的目标章节"
              value={bulkTarget}
              onChange={setBulkTarget}
              placeholder="把全部漏收段落收进…"
              items={[
                { value: '__preamble__', label: '正文前言（无编号）' },
                ...sectionItems(),
              ]}
            />
            <Btn
              size="sm"
              variant="tertiary"
              isDisabled={bulkTarget === '' || nodeCounts.missing === 0}
              onPress={bulkCollect}
            >
              收录 {nodeCounts.missing} 段漏收
            </Btn>
            <Btn
              size="sm"
              variant="outline"
              isDisabled={nodeCounts.duplicate === 0}
              onPress={dedupeAll}
            >
              合并 {nodeCounts.duplicate} 段重复
            </Btn>
          </Row>
        )}
      </SectionCard>

      {/* ------------------------------------------------------------ 工作台
          左：成品纸张（随滚动固定）。右：可操作的校正面板。
          两块必须并排：这个工具的每一次判断都是"原文这一段"对"成品这一段"，
          上下堆叠会让用户来回滚动着比对，而并排时目光不用离开纸面。
      */}
      <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <div className="min-w-0 space-y-2 xl:sticky xl:top-20">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-semibold">成品预览</h3>
          </div>
          <PreviewPane
            compact
            height="clamp(24rem, 62vh, 46rem)"
            toolbarExtra={
              <Btn size="sm" variant="ghost" onPress={onPreview}>
                全屏
              </Btn>
            }
          />
        </div>

        <div className="min-w-0">
          <Tabs aria-label="校正面板" defaultSelectedKey="edits">
            <Tabs.List>
              <Tabs.Tab id="edits">
                AI 改动
                <Badge color={editTotal > 0 ? 'warning' : 'success'}>{editTotal}</Badge>
              </Tabs.Tab>
              <Tabs.Tab id="structure">
                结构方案
                <Badge>{flat.length}</Badge>
              </Tabs.Tab>
              <Tabs.Tab id="nodes">
                原文节点
                {(nodeCounts.missing > 0 || nodeCounts.duplicate > 0) && (
                  <Badge color="danger">{nodeCounts.missing + nodeCounts.duplicate}</Badge>
                )}
              </Tabs.Tab>
              <Tabs.Tab id="verify">成文核对</Tabs.Tab>
            </Tabs.List>

            {/* AI 改动：模型声明的每一处修改、结构变动、以及手工补一条 */}
            <Tabs.Panel id="edits" className="space-y-4 pt-3">
              {/*
                结构变动面板。
                report 只保证「没有丢、没有重复」；它看不见**移动**。一段原文从一个章节
                挪到另一个章节，覆盖率仍是 100%，而成品读起来已经和原稿不同了。这里列出
                的正是这些移动，来源是「模型方案 vs 原文自身结构」的比对 —— 结构层面的
                改动日志，与文字层面的「改动汇总」对称。
              */}
              <StructurePanel />

              {/*
                改动汇总面板。
                两块内容不同且必须分开：
                  · 方案声明的改动（plan.edits）—— 待用户审阅的**意向**，可逐条接受/拒绝；
                  · 成文结果里的 diff.changed / diff.unapplied —— 服务端的**权威结论**，
                    分别表示「真的改到了成文里」与「before 没找到、没生效」。
                把两者混在一起会让用户以为声明即生效，而实际上未生效的改动是常见的。
              */}
              <SectionCard
                title="改动汇总"
                actions={
                  <Badge color={editTotal > 0 ? 'warning' : 'success'}>
                    {editTotal > 0 ? `声明改动 ${editTotal} 处` : '原文将逐字不变'}
                  </Badge>
                }
              >
                {editTotal > 0 && (
                  <Row>
                    {editKindCounts.map(([k, n]) => (
                      <Badge key={k} color="accent">
                        {editLabel(k)} {n}
                      </Badge>
                    ))}
                    <span className="flex-1" />
                    <Btn
                      size="sm"
                      variant="tertiary"
                      isDisabled={gov.verifying}
                      onPress={() => void acceptAllEdits()}
                    >
                      接受全部
                    </Btn>
                    <Btn
                      size="sm"
                      variant="danger-soft"
                      isDisabled={gov.verifying}
                      onPress={() => void rejectAllEdits()}
                    >
                      拒绝全部（还原原文）
                    </Btn>
                  </Row>
                )}

                {/* 逐条列出：before → after + 类型徽标 + 理由 + 接受/拒绝 */}
                {editTotal > 0 ? (
                  <Stack className="gap-2">
                    {indexedEditsAll.map((item) => (
                      <div key={editKey(item.edit)} className={EDIT_BLOCK}>
                        <Row>
                          <Badge color="accent">{editLabel(item.edit.kind)}</Badge>
                          <span className="font-mono text-xs text-muted">
                            {item.edit.node_id || '（无节点：插入式改动）'}
                          </span>
                          {(item.edit.occurrence ?? 0) > 0 && (
                            <span className={HINT}>第 {(item.edit.occurrence ?? 0) + 1} 处匹配</span>
                          )}
                          {/* before 找不到时这条改动一定不生效，就地告知比等导出后才发现好 */}
                          {item.edit.node_id &&
                            !beforeFound(nodeText(item.edit.node_id), item.edit) && (
                              <Badge color="warning">原文中未找到，不会生效</Badge>
                            )}
                          <span className="flex-1" />
                          <Btn
                            size="sm"
                            variant="tertiary"
                            title="确认保留这条改动并重新校验"
                            isDisabled={gov.verifying}
                            onPress={() => void acceptEdit(item.edit)}
                          >
                            接受
                          </Btn>
                          <Btn
                            size="sm"
                            variant="danger-soft"
                            title="从方案中删除这条改动，原文将保持不动"
                            onPress={() => rejectEdit(item.index, item.edit)}
                          >
                            拒绝
                          </Btn>
                        </Row>
                        <div className={EDIT_TEXT}>
                          <span className="text-danger line-through">
                            {item.edit.before || '（空）'}
                          </span>
                          <span aria-hidden="true" className="mx-1 text-muted">
                            →
                          </span>
                          <span className="text-success">{item.edit.after || '（删除该内容）'}</span>
                        </div>
                        {item.edit.reason && <div className={HINT}>理由：{item.edit.reason}</div>}
                      </div>
                    ))}
                  </Stack>
                ) : (
                  <AlertBox status="success" title="没有任何改动">
                  </AlertBox>
                )}

                {/* 插入式改动（没有 node_id）单独提示，避免用户以为界面漏显示了 */}
                {unattachedEdits.length > 0 && (
                  <AlertBox status="info" title={`另有 ${unattachedEdits.length} 处无目标节点的改动`}>
                    {unattachedEdits.map((item) => (
                      <div key={editKey(item.edit)}>
                        · {editLabel(item.edit.kind)}：{item.edit.after || item.edit.before}
                      </div>
                    ))}
                  </AlertBox>
                )}

                {/* 成文后的权威结论 */}
                {render && (
                  <>
                    {appliedEdits.length > 0 && (
                      <Stack className="gap-2">
                        <div className={HINT}>
                          以下 {appliedEdits.length} 处改动已生效并写入公文文档：
                        </div>
                        {appliedEdits.map((a, i) => (
                          <div key={`ap-${i}`} className={EDIT_BLOCK}>
                            <Row>
                              <Badge color="success">{a.label || editLabel(a.kind)}</Badge>
                              <span className="font-mono text-xs text-muted">{a.node_id}</span>
                            </Row>
                            <div className={EDIT_TEXT}>
                              <span className="text-danger line-through">{a.before}</span>
                              <span aria-hidden="true" className="mx-1 text-muted">
                                →
                              </span>
                              <span className="text-success">{a.after}</span>
                            </div>
                            {a.reason && <div className={HINT}>理由：{a.reason}</div>}
                          </div>
                        ))}
                      </Stack>
                    )}

                    {unappliedEdits.length > 0 && (
                      <Stack className="gap-2">
                        <div className="text-xs text-danger">
                          以下 {unappliedEdits.length} 处改动<strong>未能生效</strong> ——
                          待替换内容在原文中未找到：
                        </div>
                        {unappliedEdits.map((u, i) => (
                          <div key={`un-${i}`} className={EDIT_BLOCK}>
                            <Row>
                              <Badge color="warning">{editLabel(u.kind)}</Badge>
                              <span className="font-mono text-xs text-muted">
                                {u.node_id || '（无节点）'}
                              </span>
                            </Row>
                            <div className={EDIT_TEXT}>
                              <span className="text-danger line-through">{u.before}</span>
                            </div>
                            <div className={HINT}>{u.reason}</div>
                          </div>
                        ))}
                      </Stack>
                    )}
                  </>
                )}
              </SectionCard>

              {/* 手工新增改动：模型漏掉某处修改时，用户可以自己补一条 */}
              <Disclosure>
                <Disclosure.Heading className="text-sm font-medium">
                  <Disclosure.Trigger>
                    手工新增一条改动
                    <Disclosure.Indicator />
                  </Disclosure.Trigger>
                </Disclosure.Heading>
                <Disclosure.Content>
                  <Disclosure.Body className="space-y-2 pt-3">
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Field label="原文节点" htmlFor="edit-node">
                      <Select
                        id="edit-node"
                        aria-label="原文节点"
                        value={newEdit.nodeId}
                        onChange={(v) => patchNewEdit({ nodeId: v })}
                        placeholder="选择节点…"
                        items={editableNodes.map((n) => ({
                          value: n.id,
                          label: `${n.id} · ${n.text.slice(0, 30)}`,
                        }))}
                      />
                    </Field>
                    <Field label="改动类型" htmlFor="edit-kind">
                      <Select
                        id="edit-kind"
                        aria-label="改动类型"
                        value={newEdit.kind}
                        onChange={(v) => patchNewEdit({ kind: v as EditKind })}
                        items={EDIT_KINDS.map((k) => ({ value: k, label: editLabel(k) }))}
                      />
                    </Field>
                  </div>

                  {newEdit.nodeId !== '' && (
                    <Field label="所选节点的原文">
                      <div className="whitespace-pre-wrap rounded-xl border border-border bg-surface-secondary px-3 py-2 text-xs">
                        {newEditNodeText || '（无文本）'}
                      </div>
                      <Row>
                        <Btn size="sm" variant="ghost" onPress={fillBeforeFromNode}>
                          把整段原文填入「原文内容」
                        </Btn>
                      </Row>
                    </Field>
                  )}

                  <Field label="原文内容（before）" htmlFor="edit-before">
                    <TextArea
                      id="edit-before"
                      rows={2}
                      placeholder="要替换掉的原文片段，必须与原文逐字一致"
                      value={newEdit.before}
                      onChange={(e) => patchNewEdit({ before: e.target.value })}
                    />
                  </Field>

                  <Field label="改为（after）" htmlFor="edit-after">
                    <TextArea
                      id="edit-after"
                      rows={2}
                      placeholder="替换后的文字；留空表示删除该片段"
                      value={newEdit.after}
                      onChange={(e) => patchNewEdit({ after: e.target.value })}
                    />
                  </Field>

                  <Field
                    label="理由"
                    htmlFor="edit-reason"
                  >
                    <TextInput
                      id="edit-reason"
                      type="text"
                      autoComplete="off"
                      placeholder="例如：错别字，「必需」应为「必须」"
                      value={newEdit.reason}
                      onChange={(e) => patchNewEdit({ reason: e.target.value })}
                    />
                  </Field>

                  <Field
                    label="第几处匹配"
                    htmlFor="edit-occurrence"
                  >
                    <TextInput
                      id="edit-occurrence"
                      type="number"
                      min={0}
                      step={1}
                      value={newEdit.occurrence}
                      onChange={(e) => patchNewEdit({ occurrence: Number(e.target.value) })}
                    />
                  </Field>

                  {!newEditBeforeFound && (
                    <AlertBox status="warning" title="这段文字在所选节点里找不到">
                      请核对是否有多余的空格或全角/半角差异；提交这样的改动不会生效。
                    </AlertBox>
                  )}

                  <div className="flex justify-end pt-1">
                    <Btn variant="primary" isDisabled={!newEditValid} onPress={submitNewEdit}>
                      新增这条改动
                    </Btn>
                  </div>
                  </Disclosure.Body>
                </Disclosure.Content>
              </Disclosure>

            </Tabs.Panel>

            <Tabs.Panel id="structure" className="pt-3">
                {/* 右：结构方案 */}
                <SectionCard
                  compact
                  title="结构方案"
                >
                  {/*
                    正文前言：主送机关之后、第一个「一、」之前的内容。
                    刻意**不**呈现为章节、**不**显示层次序数 —— 它本就没有编号，
                    给它编一个「一、」会把第一个真实标题的序数占掉。
                  */}
                  <div className="space-y-1.5 rounded-lg border border-border bg-surface-secondary p-2.5">
                    <Row className="text-xs">
                      <span className="rounded bg-surface px-1.5 py-0.5 font-medium">前言</span>
                      <strong>正文前言（无编号）</strong>
                      <Badge>正文 {preambleRows.length} 段</Badge>
                      <Badge color="accent">不编号 · 非章节</Badge>
                    </Row>
                    {preambleRows.length === 0 ? (
                      <div className={HINT}>前言目前是空的。</div>
                    ) : (
                      <div className="space-y-1.5">
                        {preambleRows.map((p) => (
                          <div key={`pre-${p.id}-${p.index}`} className="space-y-1 rounded-md bg-surface p-2">
                            <Row className="text-xs">
                              <span className="text-muted">{p.index + 1}</span>
                              <span className="font-mono text-muted">{p.id}</span>
                              <span className={HINT}>{p.text || '（无文本）'}</span>
                              <span className="flex-1" />
                              <Btn
                                size="sm"
                                variant="ghost"
                                title="上移"
                                isDisabled={p.index === 0}
                                onPress={() => movePreambleNode(p.id, -1)}
                              >
                                ↑
                              </Btn>
                              <Btn
                                size="sm"
                                variant="ghost"
                                title="下移"
                                isDisabled={p.index === preambleRows.length - 1}
                                onPress={() => movePreambleNode(p.id, 1)}
                              >
                                ↓
                              </Btn>
                              <Btn
                                size="sm"
                                variant="ghost"
                                title="移出正文前言"
                                onPress={() => removeFromPreamble(p.id)}
                              >
                                ✕
                              </Btn>
                            </Row>
                          </div>
                        ))}
                      </div>
                    )}

                    {/* 把前言里的节点移入某个章节 */}
                    {preambleRows.length > 0 && (
                      <Row>
                        <Select
                          compact
                          className="w-auto min-w-32 max-w-48"
                          aria-label="要移出的前言节点"
                          value={relocateNode['__preamble__'] ?? preambleRows[0].id}
                          onChange={(v) => pickNode('__preamble__', v)}
                          items={preambleRows.map((p) => ({ value: p.id, label: p.id }))}
                        />
                        <span className={HINT}>移入章节：</span>
                        <Select
                          compact
                          className="w-auto min-w-44 max-w-56"
                          aria-label="移入哪个章节"
                          value={relocateTo['__preamble__'] ?? ''}
                          onChange={(v) => pickRelocateTo('__preamble__', v)}
                          placeholder="选择章节…"
                          items={sectionItems()}
                        />
                        <Btn
                          size="sm"
                          variant="ghost"
                          isDisabled={(relocateTo['__preamble__'] ?? '') === ''}
                          onPress={() =>
                            movePreambleTo(
                              relocateNode['__preamble__'] ?? preambleRows[0].id,
                              relocateTo['__preamble__'] ?? '',
                            )
                          }
                        >
                          移入
                        </Btn>
                      </Row>
                    )}

                    <Row>
                      <Btn
                        size="sm"
                        variant="tertiary"
                        isDisabled={nodeCounts.missing === 0}
                        title="把当前全部漏收段落按原文顺序收进前言"
                        onPress={collectAllToPreamble}
                      >
                        把 {nodeCounts.missing} 段漏收收进前言
                      </Btn>
                    </Row>
                  </div>

                  {/* 章节列表 */}
                  <Stack className="gap-3">
                    {flat.map((f) => {
                      const s = f.section
                      const movable = movableOf(s)
                      /**
                       * 标题是否已与原文不一致。
                       *
                       * 这里用**本地**逐字比较而不是只等服务端 diagnostic：editPlan 是
                       * 防抖 700ms 才提交的，若只看报告，用户改完标题的那一瞬间界面毫无
                       * 反馈；原文里的全角空格也必须精确比较，故只 trim 首尾。
                       */
                      const titleMismatch =
                        !!s.heading_node_id &&
                        s.title.trim() !== nodeText(s.heading_node_id).trim()
                      return (
                        <div
                          key={s.id}
                          className="space-y-2 rounded-lg border border-border bg-surface p-2.5"
                          style={{ marginLeft: `${Math.min(f.depth, 4) * 0.9}rem` }}
                        >
                          <Row className="gap-x-2">
                            <Badge>L{s.level}</Badge>
                            <Badge>正文 {(s.body_node_ids ?? []).length} 段</Badge>
                            {s.heading_node_id ? (
                              <Badge color="success">标题取自原文 {s.heading_node_id}</Badge>
                            ) : (
                              <Badge color="warning">模型自拟标题</Badge>
                            )}
                            {titleMismatch && <Badge color="danger">标题与原文不符</Badge>}
                            {sectionHasError(s) && <Badge color="danger">有严重问题</Badge>}
                          </Row>

                          <Field label="章节标题" htmlFor={`title-${s.id}`}>
                            <TextInput
                              id={`title-${s.id}`}
                              type="text"
                              autoComplete="off"
                              aria-label={`章节 ${s.id} 标题`}
                              data-invalid={titleMismatch || undefined}
                              value={s.title}
                              onChange={(e) => rename(s.id, e.target.value)}
                            />
                          </Field>

                          {s.heading_node_id && (
                            <Row className="gap-x-2 gap-y-1 text-xs">
                              <Btn size="sm" variant="secondary" onPress={() => restoreHeadingTitle(s.id)}>
                                改回原文
                              </Btn>
                              <Btn size="sm" variant="danger-soft" onPress={() => detachHeading(s.id)}>
                                改为自拟标题
                              </Btn>
                            </Row>
                          )}

                          {/* 标题与原文不符：给出可执行的下一步，而不是只报错 */}
                          {titleMismatch && (
                            <AlertBox status="danger" title="标题必须与原文逐字一致，否则无法通过校验">
                              <div>原文：{nodeText(s.heading_node_id ?? '')}</div>
                              <div>请「改回原文」，或「改为自拟标题」（放弃与原文的绑定）。</div>
                            </AlertBox>
                          )}

                          {s.rationale && <div className={HINT}>模型理由：{s.rationale}</div>}

                          <div className="space-y-1.5">
                            {(s.body_node_ids ?? []).length > 0 ? (
                              (s.body_node_ids ?? []).map((nid, i, arr) => (
                                <div key={`${s.id}-${nid}-${i}`} className="rounded-md bg-surface-secondary p-2">
                                  <Row className="text-xs">
                                    <span className="text-muted">{i + 1}</span>
                                    <span className="font-mono text-muted">{nid}</span>
                                    <span className="min-w-0 flex-1 truncate" title={nodeText(nid)}>
                                      {nodeText(nid) || '（无文本）'}
                                    </span>
                                    <Btn
                                      size="sm"
                                      variant="ghost"
                                      title="上移"
                                      isDisabled={i === 0}
                                      onPress={() => move(s.id, nid, -1)}
                                    >
                                      ↑
                                    </Btn>
                                    <Btn
                                      size="sm"
                                      variant="ghost"
                                      title="下移"
                                      isDisabled={i === arr.length - 1}
                                      onPress={() => move(s.id, nid, 1)}
                                    >
                                      ↓
                                    </Btn>
                                    <Btn
                                      size="sm"
                                      variant="ghost"
                                      title="从此章节移出"
                                      onPress={() => detach(s.id, nid)}
                                    >
                                      ✕
                                    </Btn>
                                  </Row>
                                </div>
                              ))
                            ) : (
                              <div className={HINT}>本节还没有收录任何原文段落。</div>
                            )}
                          </div>

                          {/* 章节级操作：把本节点移入其他章节 */}
                          <Row className="gap-x-2 gap-y-1">
                            <Select
                              compact
                              className="w-auto min-w-48 max-w-56"
                              aria-label="把本节点移入其他章节"
                              value={relocateTargetOf(s.id)}
                              onChange={(v) => pickRelocateTo(s.id, v)}
                              placeholder="把本节点移入其他章节…"
                              items={sectionItems(s.id)}
                            />
                            <span className={HINT}>源节点：</span>
                            <Select
                              compact
                              className="w-auto min-w-28 max-w-56"
                              aria-label="要移入的源节点"
                              isDisabled={(s.body_node_ids ?? []).length === 0}
                              value={movable}
                              onChange={(v) => pickNode(s.id, v)}
                              items={(s.body_node_ids ?? []).map((nid) => ({ value: nid, label: nid }))}
                            />
                            <Btn
                              size="sm"
                              variant="ghost"
                              isDisabled={
                                (s.body_node_ids ?? []).length === 0 || relocateTargetOf(s.id) === ''
                              }
                              onPress={() => relocate(s.id, movable, relocateTargetOf(s.id))}
                            >
                              移入
                            </Btn>
                          </Row>

                          {/* 若本节标题是模型自拟的，允许把某段原文标题关联进来 */}
                          {!s.heading_node_id && (
                            <Row className="gap-x-2 gap-y-1">
                              <Select
                                compact
                                className="w-auto min-w-56 max-w-64"
                                aria-label="把某段原文标题关联为本节标题"
                                value={attachNode[s.id] ?? ''}
                                onChange={(v) => pickHeading(s.id, v)}
                                placeholder="把某段原文标题关联为本节标题…"
                                items={sourceHeadings.map((n) => ({
                                  value: n.id,
                                  label: `${n.id} · ${n.text.slice(0, 24)}`,
                                }))}
                              />
                              <Btn
                                size="sm"
                                variant="ghost"
                                isDisabled={(attachNode[s.id] ?? '') === ''}
                                onPress={() => {
                                  const id = attachNode[s.id] ?? ''
                                  if (id !== '') attachHeading(s.id, id)
                                }}
                              >
                                关联并同步标题
                              </Btn>
                            </Row>
                          )}
                        </div>
                      )
                    })}

                    {flat.length === 0 && (
                      <EmptyState>
                        <div>方案里没有任何章节。</div>
                      </EmptyState>
                    )}
                  </Stack>

                  <Row>
                    <TextInput
                      type="text"
                      autoComplete="off"
                      placeholder="新章节标题（自拟）"
                      value={newTitle}
                      onChange={(e) => setNewTitle(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') addSection()
                      }}
                    />
                    <Btn size="sm" variant="secondary" onPress={addSection}>
                      新建章节
                    </Btn>
                  </Row>
                </SectionCard>
            </Tabs.Panel>

            <Tabs.Panel id="nodes" className="pt-3">
                {/* 左：原文节点 */}
                <SectionCard
                  compact
                  title="原文节点"
                >
                  <TextInput
                    type="search"
                    placeholder="按原文内容或节点号筛选…"
                    autoComplete="off"
                    value={keyword}
                    onChange={(e) => setKeyword(e.target.value)}
                  />

                  {/* 筛选是互斥单选，交给 ToggleButtonGroup 处理 roving tabindex
                      与 aria；手写一排 aria-pressed 的按钮读屏器只当普通按钮。 */}
                  <ToggleButtonGroup
                    aria-label="按状态筛选原文节点"
                    selectionMode="single"
                    selectedKeys={[nodeFilter]}
                    onSelectionChange={(keys) => {
                      const next = [...keys][0]
                      if (next == null) return
                      setNodeFilter(String(next) as NodeFilter)
                    }}
                    className="self-start"
                  >
                    {(
                      [
                        { k: 'all', label: '全部' },
                        { k: 'missing', label: `漏收 ${nodeCounts.missing}` },
                        { k: 'duplicate', label: `重复 ${nodeCounts.duplicate}` },
                        { k: 'edited', label: `改动 ${nodeCounts.edited}` },
                        { k: 'heading', label: '原文标题' },
                        { k: 'preamble', label: `前言 ${preambleRows.length}` },
                      ] as { k: NodeFilter; label: string }[]
                    ).map((f) => (
                      <ToggleButton key={f.k} id={f.k} size="sm" className="px-3">
                        {f.label}
                      </ToggleButton>
                    ))}
                  </ToggleButtonGroup>

                  <div className="max-h-[36rem] space-y-2 overflow-auto pr-1">
                    {nodeRows.map((row) => {
                      const target = targetFor(row.node.id)
                      return (
                        <div
                          key={row.node.id}
                          data-kind={row.node.kind}
                          data-status={row.status}
                          className="space-y-1.5 rounded-lg border border-border bg-surface p-2.5"
                        >
                          <Row className="gap-x-2 gap-y-1 text-xs">
                            <span className="font-mono text-muted">{row.node.id}</span>
                            <span>{KIND_LABEL[row.node.kind]}</span>
                            {row.node.source_style && <span>· {row.node.source_style}</span>}
                            {/* 严重问题优先显示；mismatch 必须排在 headingOf 之前，
                                否则「标题不符」会被绿色的「用作标题」徽标盖住。 */}
                            {nodeStatusBadge(row)}
                            {row.edits.length > 0 && (
                              <Badge color="warning">有改动 {row.edits.length} 处</Badge>
                            )}
                            {editKindBadges(row.node.id).map((k) => (
                              <Badge key={k} color="accent">
                                {editLabel(k)}
                              </Badge>
                            ))}
                          </Row>

                          {/* 就地 diff：有改动时显示「原文 → 改动后」，没有改动时就是逐字原文。
                              任何一种情况都不做 trim —— 原文里的全角空格与行尾空格都是内容的一部分。 */}
                          {row.rendered ? (
                            <>
                              <div className="whitespace-pre-wrap text-sm">
                                <span className="mr-1 rounded bg-danger-soft px-1 text-xs text-danger">
                                  原文
                                </span>
                                {row.node.text || '（无文本）'}
                              </div>
                              <div className="whitespace-pre-wrap text-sm">
                                <span className="mr-1 rounded bg-success-soft px-1 text-xs text-success">
                                  改动后
                                </span>
                                {row.rendered.after || '（无文本）'}
                              </div>
                              {row.rendered.failed > 0 && (
                                <div className="text-xs text-danger">
                                  有 {row.rendered.failed} 处改动的「原文内容」在该段里找不到，不会生效。
                                </div>
                              )}
                            </>
                          ) : (
                            <div className="whitespace-pre-wrap text-sm">
                              {row.node.text || '（无文本）'}
                            </div>
                          )}

                          {/* 逐条改动的 before → after、理由与接受/拒绝 */}
                          {row.edits.length > 0 && (
                            <Stack className="gap-2">
                              {row.edits.map((item) => (
                                <div key={editKey(item.edit)} className={EDIT_BLOCK}>
                                  <Row>
                                    <Badge color="accent">{editLabel(item.edit.kind)}</Badge>
                                    {!beforeFound(row.node.text, item.edit) && (
                                      <Badge color="warning">原文中未找到</Badge>
                                    )}
                                    <span className="flex-1" />
                                    <Btn
                                      size="sm"
                                      variant="tertiary"
                                      isDisabled={gov.verifying}
                                      onPress={() => void acceptEdit(item.edit)}
                                    >
                                      接受
                                    </Btn>
                                    <Btn
                                      size="sm"
                                      variant="danger-soft"
                                      title="删除这条改动，该段保持原文"
                                      onPress={() => rejectEdit(item.index, item.edit)}
                                    >
                                      拒绝
                                    </Btn>
                                  </Row>
                                  <div className={EDIT_TEXT}>
                                    <span className="text-danger line-through">{item.edit.before}</span>
                                    <span aria-hidden="true" className="mx-1 text-muted">
                                      →
                                    </span>
                                    <span className="text-success">
                                      {item.edit.after || '（删除）'}
                                    </span>
                                  </div>
                                  {item.edit.reason && (
                                    <div className={HINT}>理由：{item.edit.reason}</div>
                                  )}
                                </div>
                              ))}
                            </Stack>
                          )}

                          {row.issue?.messages?.length ? (
                            <Row className="text-xs">
                              <span>{row.issue.messages[0]}</span>
                            </Row>
                          ) : null}

                          {/* 修复动作：漏收 → 收录到某章节；重复 → 只保留一处 */}
                          <Row className="gap-x-2 gap-y-1">
                            {row.counts && row.status === 'missing' ? (
                              <>
                                <Select
                                  compact
                                  className="w-auto min-w-44 max-w-52"
                                  aria-label={`把 ${row.node.id} 收录到哪个章节`}
                                  value={target}
                                  onChange={(v) => setCollectTargetFor(row.node.id, v)}
                                  placeholder="收录到…"
                                  items={[
                                    /* 前言排在第一位：缘由段是最常见的漏收内容，且它本就不该有章节号 */
                                    { value: '__preamble__', label: '正文前言（无编号）' },
                                    ...sectionItems(),
                                  ]}
                                />
                                <Btn
                                  size="sm"
                                  variant="primary"
                                  isDisabled={target === ''}
                                  onPress={() => {
                                    if (target === '__preamble__') addToPreamble(row.node)
                                    else collectNode(row.node)
                                  }}
                                >
                                  {target === '__preamble__' ? '移入前言' : '收录'}
                                </Btn>
                              </>
                            ) : row.claims.length > 1 ? (
                              <>
                                <Btn
                                  size="sm"
                                  variant="tertiary"
                                  onPress={() => keepFirstOnly(row.node)}
                                >
                                  只保留第一处
                                </Btn>
                                {row.claims.map((site, i) => (
                                  <Btn
                                    key={`${site.kind}-${site.sectionId}-${i}`}
                                    size="sm"
                                    variant="ghost"
                                    title={`去掉在「${site.label}」的收录`}
                                    onPress={() => dropClaim(row.node.id, site)}
                                  >
                                    移出第 {i + 1} 处（{site.label}）
                                  </Btn>
                                ))}
                              </>
                            ) : preambleHas(plan, row.node.id) ? (
                              <Btn
                                size="sm"
                                variant="ghost"
                                onPress={() => removeFromPreamble(row.node.id)}
                              >
                                移出正文前言
                              </Btn>
                            ) : row.elementRole ? (
                              <Btn
                                size="sm"
                                variant="ghost"
                                title="解除后该节点可被当作正文收录进章节"
                                onPress={() => unbindElement(row.node.id)}
                              >
                                解除要素绑定
                              </Btn>
                            ) : row.headingOf ? (
                              <>
                                <Btn
                                  size="sm"
                                  variant="ghost"
                                  onPress={() => {
                                    if (row.headingOf) detachHeading(row.headingOf.id)
                                  }}
                                >
                                  解除标题关联
                                </Btn>
                                {row.status === 'mismatch' && (
                                  <Btn
                                    size="sm"
                                    variant="secondary"
                                    onPress={() => {
                                      if (row.headingOf) restoreHeadingTitle(row.headingOf.id)
                                    }}
                                  >
                                    把标题改回原文
                                  </Btn>
                                )}
                              </>
                            ) : null}
                          </Row>
                        </div>
                      )
                    })}

                    {nodeRows.length === 0 && (
                      <EmptyState>
                        <div>没有符合条件的节点。</div>
                      </EmptyState>
                    )}
                  </div>
                </SectionCard>

            </Tabs.Panel>

            <Tabs.Panel id="verify" className="space-y-4 pt-3">
              {/* 还没成文时不能留一张空白页：用户点进来是因为想核对成品，
                  所以要告诉他核对的对象还不存在、以及怎么让它存在。 */}
              {!render && (
                <SectionCard title="成文核对">
                  <EmptyState>
                    <div>还没有成文，暂时没有可核对的结果。</div>
                  </EmptyState>
                </SectionCard>
              )}

              {/* 成文结果与逐字比对 */}
              {render && (
                <SectionCard
                  title="成文结果"
                  actions={
                    <Row>
                      <Btn
                        size="sm"
                        variant="ghost"
                        isDisabled={gov.rendering || !canExport}
                        title={canExport ? '' : '方案有未提交的修改或校验未通过'}
                        onPress={() => void exportDocx()}
                      >
                        重新生成
                      </Btn>
                      <Btn
                        variant="primary"
                        isDisabled={!downloadReady}
                        title={downloadWhy}
                        onPress={downloadDocx}
                      >
                        下载公文文档
                      </Btn>
                    </Row>
                  }
                >
                  {!downloadReady ? (
                    <AlertBox status="warning" title="下载已暂停">
                      {downloadWhy}
                    </AlertBox>
                  ) : !diffHasProblems ? (
                    /* 校验通过 + 没有未声明偏差 = 可以直接交付。
                       这里刻意用一句话把结论说透，用户不必自己解读三列数字。 */
                    <AlertBox status="success" title="逐字比对通过，可以交付">
                    </AlertBox>
                  ) : (
                    <AlertBox status="danger" title="逐字比对发现偏差，请勿直接交付">
                    </AlertBox>
                  )}

                  <div className="grid gap-3 lg:grid-cols-3">
                    <div className="space-y-1.5 rounded-lg border border-border bg-surface p-2.5">
                      <div className={`text-xs font-medium ${missingTexts.length > 0 ? 'text-danger' : ''}`}>
                        缺失（必须为 0）: {missingTexts.length}
                      </div>
                      {missingTexts.map((m, i) => (
                        <div key={`missing-${i}`} className="whitespace-pre-wrap text-sm text-danger">
                          {m}
                        </div>
                      ))}
                      {missingTexts.length === 0 && <div className={HINT}>原文无缺失。</div>}
                    </div>

                    <div className="space-y-1.5 rounded-lg border border-border bg-surface p-2.5">
                      <div className={`text-xs font-medium ${alteredTexts.length > 0 ? 'text-warning' : ''}`}>
                        被改写（必须为 0）: {alteredTexts.length}
                      </div>
                      {alteredTexts.map((a, i) => (
                        <div key={`altered-${i}`} className="whitespace-pre-wrap text-sm text-warning">
                          {a}
                        </div>
                      ))}
                      {alteredTexts.length === 0 && (
                        <div className={HINT}>没有任何原文被未声明地改写。</div>
                      )}
                    </div>

                    <div className="space-y-1.5 rounded-lg border border-border bg-surface p-2.5">
                      <div className="text-xs font-medium">
                        多出（应为装配器生成的脚手架）: {extraTexts.length}
                      </div>
                      {extraTexts.map((x, i) => (
                        <div key={`extra-${i}`} className="whitespace-pre-wrap text-sm">
                          {x}
                        </div>
                      ))}
                      {extraTexts.length === 0 && <div className={HINT}>没有额外文本。</div>}
                    </div>
                  </div>

                  <Row>
                    <Badge color={appliedEdits.length > 0 ? 'success' : 'default'}>
                      已生效改动 {appliedEdits.length} 处
                    </Badge>
                    <Badge color={unappliedEdits.length > 0 ? 'warning' : 'default'}>
                      未生效改动 {unappliedEdits.length} 处
                    </Badge>
                  </Row>

                  {(render.synth_text?.length ?? 0) > 0 && (
                    <div className={HINT}>
                      装配器生成的文本（层次序数、版头等）：{(render.synth_text ?? []).join('｜')}
                    </div>
                  )}

                  {(plan.notes?.length ?? 0) > 0 && (
                    <AlertBox status="info" title="模型的说明">
                      {(plan.notes ?? []).map((n, i) => (
                        <div key={i}>· {n}</div>
                      ))}
                    </AlertBox>
                  )}
                </SectionCard>
              )}
            </Tabs.Panel>
          </Tabs>
        </div>
      </div>
    </section>
  )
}
