/**
 * 单页工作台。
 *
 * 上传后不再分四步：左边是原文预览与结构，右边是待校对改动与对话。
 * 模型给出的任何文字修改都先进入校对栏；用户点「同意」才写回方案并刷新
 * 预览，点「不同意」则丢掉这条建议，原文保持不变。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Switch, cn } from '@heroui/react'
import {
  editLabel,
  type BodyOrder,
  type Edit,
  type ElementChange,
  type Meta,
  type MetaChange,
  type NodeSummary,
  type SectionChange,
  type SectionMove,
  type StructureMove,
} from '../api/types'
import {
  headingTag,
  levelLabel,
  outlineFromNodes,
  outlineFromPlan,
  type OutlineItem,
} from '../lib/headings'
import { readFrameSelection, type SelectionMatch } from '../lib/selection'
import {
  acceptDeclaredEdit,
  acceptPendingEdit,
  acceptRecompose,
  acceptStructure,
  dismissPendingEdit,
  dissolveSynthesizedSection,
  editSignature,
  gov,
  loadSource,
  notify,
  prepareDocument,
  reanalyze,
  rejectDeclaredEdit,
  rejectRecompose,
  requestRevision,
  restoreBodyOrder,
  restoreElementBinding,
  restoreElementToBody,
  restoreMetaField,
  restoreMissingSection,
  restoreMoveTarget,
  restorePreambleOrder,
  restoreSectionLevel,
  restoreSectionOrder,
  restoreSectionParent,
  restoreSectionShape,
  restoreSectionTitle,
  revertAddedSectionToBody,
  startAnalysis,
  uploadDocument,
} from '../store/govdoc'
import type { AppliedEdit } from '../store/govdoc'
import type { Recompose } from '../api/types'
import type { PendingRecompose } from '../lib/recompose'
import { useGov } from '../store/useGov'
import PreviewPane from './PreviewPane'
import {
  Badge,
  Btn,
  IndeterminateProgress,
  InlineSpinner,
  Row,
  TextArea,
} from '../ui/primitives'

/**
 * 预览位置的空态上传区。
 *
 * 工作台从空白就存在，上传不是另一个页面，而是替掉左边这张纸的位置。
 * 右侧校对与对话保持为空框，用户上传后同一套布局直接接上。
 */
function PreviewUpload({
  uploading,
  onPick,
  onDrop,
}: {
  uploading: boolean
  onPick: () => void
  onDrop: (file: File | undefined) => void
}) {
  const [dragging, setDragging] = useState(false)

  return (
    <div
      data-active={dragging ? 'true' : 'false'}
      onDragOver={(e) => {
        e.preventDefault()
        setDragging(true)
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => {
        e.preventDefault()
        setDragging(false)
        onDrop(e.dataTransfer?.files?.[0])
      }}
      className="flex h-full min-h-[24rem] items-center justify-center rounded-2xl border border-dashed border-border bg-surface p-3 transition-colors data-[active=true]:border-accent data-[active=true]:bg-accent-soft"
    >
      <button
        type="button"
        onClick={onPick}
        disabled={uploading}
        className="flex min-h-[20rem] w-full flex-col items-center justify-center gap-3 rounded-xl px-6 py-10 text-center outline-none transition-colors hover:bg-surface-secondary focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-default"
      >
        {uploading ? (
          <>
            <InlineSpinner className="size-6" />
            <span className="text-sm font-medium">正在解析文件…</span>
          </>
        ) : (
          <>
            <span
              aria-hidden="true"
              className="grid size-14 place-items-center rounded-2xl bg-accent-soft text-2xl text-accent-soft-foreground"
            >
              ↑
            </span>
            <span className="text-base font-semibold">点击上传文档，或拖到这里</span>
          </>
        )}
      </button>
    </div>
  )
}

/** 浮动结构栏：最多三级标题，并可开关预览高亮。 */
function OutlineRail({
  outline,
  highlight,
  onHighlight,
  onJump,
}: {
  outline: OutlineItem[]
  highlight: boolean
  onHighlight: (v: boolean) => void
  onJump: (item: OutlineItem) => void
}) {
  return (
    <aside className="w-60 shrink-0 self-start rounded-2xl border border-border bg-surface/95 p-3 shadow-lg backdrop-blur">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-xs font-semibold">文档结构</span>
        <span className="flex items-center gap-1.5">
          <span className="text-[11px] text-muted">标出级别</span>
          <Switch
            size="sm"
            isSelected={highlight}
            onChange={onHighlight}
            aria-label="在预览中标出标题级别"
          >
            <Switch.Content>
              <Switch.Control />
              <span className="sr-only">在预览中标出标题级别</span>
            </Switch.Content>
          </Switch>
        </span>
      </div>
      <div className="max-h-[52vh] space-y-0.5 overflow-auto pr-1">
        {outline.length === 0 ? null : (
          outline.map((item) => (
            <button
              type="button"
              key={item.key}
              onClick={() => onJump(item)}
              disabled={item.nodeId === ''}
              title={item.nodeId === '' ? '该标题由模型生成，原文中没有可跳转的段落' : '跳转到该标题'}
              className="flex w-full items-start gap-1.5 rounded-lg px-1.5 py-1 text-left text-[13px] leading-snug transition-colors hover:bg-surface-secondary focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none disabled:cursor-default disabled:opacity-40"
              style={{ paddingLeft: `${0.35 + (item.level - 1) * 0.9}rem` }}
            >
              <span
                className={cn(
                  'mt-0.5 shrink-0 rounded px-1 text-[10px] font-semibold',
                  item.level === 1 && 'bg-accent-soft text-accent-soft-foreground',
                  item.level === 2 && 'bg-warning-soft text-warning-soft-foreground',
                  item.level === 3 && 'bg-success-soft text-success-soft-foreground',
                )}
              >
                {headingTag(item.level)}
              </span>
              <span className="min-w-0 break-words">{item.title}</span>
            </button>
          ))
        )}
      </div>
    </aside>
  )
}

/**
 * 一条待审综合项。
 *
 * 校对区原先只收文字改动与整段重组，于是模型搬段、加章、改标题、把段落绑成
 * 公文要素这些**结构**上的动作一个都不会出现在这里 —— 用户只能靠肉眼比对。
 * 结构项因此也是一等条目，并且带一个可执行的还原动作。
 */
type ReviewItem =
  | { kind: 'edit'; edit: Edit; pending: boolean; index: number; provisional: boolean }
  | {
      kind: 'recompose'
      rec: Recompose
      pending: PendingRecompose | null
      index: number
      provisional: boolean
    }
  | { kind: 'move'; move: StructureMove; provisional: boolean }
  | { kind: 'section'; change: SectionChange; provisional: boolean }
  | { kind: 'section-move'; move: SectionMove; provisional: boolean }
  | { kind: 'element'; binding: ElementChange; provisional: boolean }
  | { kind: 'meta'; change: MetaChange; provisional: boolean }
  | { kind: 'body-order'; order: BodyOrder; provisional: boolean }

/** 待审改动：同意即写入方案，不同意直接丢掉。 */
function ReviewList() {
  useGov()
  const [busy, setBusy] = useState('')

  const declared = useMemo<ReviewItem[]>(() => {
    const handled = new Set(gov.handledEdits)
    return (gov.plan?.edits ?? [])
      .filter((e) => !handled.has(editSignature(e)))
      .map((e) => ({
        kind: 'edit' as const,
        edit: e,
        pending: false,
        index: -1,
        provisional: false,
      }))
  }, [gov.plan, gov.handledEdits])

  const pending = gov.pendingEdits.map<ReviewItem>((e, index) => ({
    kind: 'edit',
    edit: e,
    pending: true,
    index,
    provisional: false,
  }))

  const recompose = gov.pendingRecompose.map<ReviewItem>((item, index) => ({
    kind: 'recompose',
    rec: item.recompose,
    pending: item,
    index,
    provisional: false,
  }))

  // 结构调整：换章节、章节增删改名/改层级、整章换位置、要素绑定、自拟要素、
  // 节内与前言顺序。它们同样必须逐条出现在校对栏，而不是只在别处汇总。
  const structure = useMemo<ReviewItem[]>(() => {
    const diff = gov.structure
    if (!diff) return []
    const handled = new Set(gov.handledStructure)
    const out: ReviewItem[] = []
    const push = (item: ReviewItem): void => {
      if (!handled.has(reviewKey(item))) out.push(item)
    }

    // 原文没有分章时，moves 已由服务端按「归入哪一章」汇总，仍然要显示：
    // 否则这种最常见的上传会一条结构项都没有。
    for (const move of diff.moves ?? []) push({ kind: 'move', move, provisional: false })
    if (!diff.baseline_flat) {
      for (const move of diff.moved_sections ?? []) {
        push({ kind: 'section-move', move, provisional: false })
      }
    }
    for (const change of diff.sections ?? []) {
      push({ kind: 'section', change, provisional: false })
    }
    for (const binding of diff.elements ?? []) push({ kind: 'element', binding, provisional: false })
    for (const change of diff.meta ?? []) push({ kind: 'meta', change, provisional: false })
    for (const order of diff.body_orders ?? []) push({ kind: 'body-order', order, provisional: false })
    return out
  }, [gov.structure, gov.handledStructure])

  // 生成中的校对项先显示出来，但不允许处理：此时方案还没有通过最终校验，
  // 同意一条也只会写进一份尚未完整的方案。
  const streaming: ReviewItem[] =
    gov.analyzing && (gov.partialEdits.length > 0 || gov.partialRecompose.length > 0)
      ? [
          ...gov.partialRecompose.map((rec, index) => ({
            kind: 'recompose' as const,
            rec,
            pending: null,
            index,
            provisional: true,
          })),
          ...gov.partialEdits.map((e, index) => ({
            kind: 'edit' as const,
            edit: e,
            pending: true,
            index,
            provisional: true,
          })),
        ]
      : []
  const items = gov.analyzing
    ? streaming
    : [...structure, ...recompose, ...pending, ...declared]

  async function accept(item: ReviewItem): Promise<void> {
    const key = reviewKey(item)
    setBusy(key)
    switch (item.kind) {
      case 'recompose':
        await acceptRecompose(item.index)
        break
      case 'edit':
        if (item.pending) await acceptPendingEdit(item.index)
        else await acceptDeclaredEdit(item.edit)
        break
      default:
        await acceptStructure(key)
        break
    }
    setBusy('')
  }

  async function reject(item: ReviewItem): Promise<void> {
    const key = reviewKey(item)
    setBusy(key)
    switch (item.kind) {
      case 'recompose':
        rejectRecompose(item.index)
        notify('info', '已拒绝这项重组，原文保持不变。')
        break
      case 'edit':
        if (item.pending) {
          dismissPendingEdit(item.index)
          notify('info', '已拒绝这项改动，原文保持不变。')
        } else {
          await rejectDeclaredEdit(item.edit)
        }
        break
      default:
        revertStructure(item)
        break
    }
    setBusy('')
  }

  if (items.length === 0) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-muted">
        {gov.analyzing ? (
          <span className="flex items-center gap-2">
            <InlineSpinner className="size-4" />
            正在生成校对项
          </span>
        ) : gov.lastHandled ? (
          resultText(gov.lastHandled)
        ) : gov.lastHandledRecompose ? (
          recomposeResultText(
            gov.lastHandledRecompose.rec,
            gov.lastHandledRecompose.accepted,
          )
        ) : (
          '暂无待校对项'
        )}
      </div>
    )
  }

  return (
    <div className="h-full space-y-2 overflow-auto pr-1">
      <div className="flex flex-wrap items-center gap-1.5 rounded-xl bg-surface-secondary px-3 py-1.5 text-[11px] text-muted">
        <span>共 {items.length} 项</span>
        <span>·</span>
        <span>结构 {structure.length}</span>
        <span>·</span>
        <span>重组 {recompose.length}</span>
        <span>·</span>
        <span>文字 {pending.length + declared.length}</span>
      </div>
      {gov.lastHandled && (
        <div className="flex items-center gap-2 rounded-xl bg-surface-secondary px-3 py-2 text-xs text-muted">
          <span className="min-w-0 flex-1">{resultText(gov.lastHandled)}</span>
        </div>
      )}
      {gov.lastHandledRecompose && (
        <div className="flex items-center gap-2 rounded-xl bg-surface-secondary px-3 py-2 text-xs text-muted">
          <span className="min-w-0 flex-1">
            {recomposeResultText(gov.lastHandledRecompose.rec, gov.lastHandledRecompose.accepted)}
          </span>
        </div>
      )}
      {items.map((item) => {
        const key = reviewKey(item)
        if (item.kind === 'recompose') {
          return (
            <RecomposeCard
              key={key}
              rec={item.rec}
              provisional={item.provisional}
              busy={busy === key}
              onAccept={() => void accept(item)}
              onReject={() => void reject(item)}
            />
          )
        }
        if (item.kind !== 'edit') {
          return (
            <StructureCard
              key={key}
              item={item}
              busy={busy === key}
              onAccept={() => void accept(item)}
              onReject={() => void reject(item)}
            />
          )
        }
        const edit = item.edit
        const itemSignature = editSignature(edit)
        return (
          <div
            key={key}
            className="space-y-2 rounded-2xl border border-border bg-surface p-3"
          >
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <Badge color={item.pending ? 'accent' : 'warning'}>
                {item.provisional ? '生成中' : item.pending ? '对话新增' : '原文修改'}
              </Badge>
              <Badge>{editLabel(edit.kind)}</Badge>
              <span className="font-mono text-muted">{edit.node_id || '（无节点）'}</span>
            </div>
            <div className="space-y-1 text-sm leading-relaxed">
              <div className="whitespace-pre-wrap text-danger line-through">
                {edit.before || '（空）'}
              </div>
              <div className="whitespace-pre-wrap text-success">{edit.after || '（删除）'}</div>
            </div>
            {edit.reason ? <div className="text-xs text-muted">{edit.reason}</div> : null}
            {!item.provisional && (
              <Row className="justify-end">
                <Button
                  size="sm"
                  variant="danger-soft"
                  isDisabled={busy === itemSignature}
                  onPress={() => void reject(item)}
                >
                  不同意
                </Button>
                <Button
                  size="sm"
                  variant="primary"
                  isDisabled={busy === itemSignature}
                  onPress={() => void accept(item)}
                >
                  {busy === itemSignature && <InlineSpinner className="size-3" />}
                  同意
                </Button>
              </Row>
            )}
          </div>
        )
      })}
    </div>
  )
}

/** 校对项在本次渲染里的稳定键；同一条目的按钮 spinner 也用它。 */
function reviewKey(item: ReviewItem): string {
  switch (item.kind) {
    case 'recompose':
      return `${item.provisional ? 'sr' : 'r'}-${item.rec.node_id}-${item.index}`
    case 'edit':
      return `${item.provisional ? 'se' : item.pending ? 'pe' : 'de'}-${editSignature(item.edit)}-${item.index}`
    case 'move':
      return `mv-${item.move.node_id}-${item.move.kind}-${item.move.from_id}-${item.move.to_id}`
    case 'section':
      return `sc-${item.change.kind}-${item.change.id}-${item.change.title}`
    case 'section-move':
      return `sm-${item.move.id}-${item.move.baseline_pos}-${item.move.rendered_pos}`
    case 'element':
      return `el-${item.binding.kind}-${item.binding.role}-${item.binding.node_id}`
    case 'meta':
      return `me-${item.change.field}-${item.change.value}`
    case 'body-order':
      return `bo-${item.order.kind}-${item.order.section_id ?? ''}`
  }
}

/** 结构变动的中文标签；未知值回显原值，界面不出现裸英文。 */
const MOVE_LABEL: Record<string, string> = {
  moved: '换了章节',
  to_preamble: '移入正文前言',
  from_preamble: '从前言移入章节',
  to_element: '改作公文要素',
}

const SECTION_LABEL: Record<string, string> = {
  added: '新增章节',
  missing: '原文有、方案没有',
  retitled: '标题被改写',
  releveled: '层级被调整',
  reparented: '换了上级章节',
  synthesized: '模型自拟标题',
}

/**
 * 一条结构调整的卡片。
 *
 * 与文字改动同样给出「原文 → 成品」，但动作的语义不同：同意只是确认这个结构
 * （方案里本来就是它），不同意则把这段/这章按原文还原。不能把两者混成一个
 * 沉默的勾选，否则用户无法判断自己到底改变了什么。
 */
function StructureCard({
  item,
  busy,
  onAccept,
  onReject,
}: {
  item: Exclude<ReviewItem, { kind: 'edit' } | { kind: 'recompose' }>
  busy: boolean
  onAccept: () => void
  onReject: () => void
}) {
  const { tag, detail, undo, canUndo } = structureText(item)
  return (
    <div className="space-y-2 rounded-2xl border border-border bg-surface p-3">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <Badge color="accent">{tag}</Badge>
        {detail.id ? <span className="font-mono text-muted">{detail.id}</span> : null}
      </div>
      {detail.before !== null ? (
        <div className="space-y-1 text-sm leading-relaxed">
          <div className="whitespace-pre-wrap text-danger line-through">{detail.before}</div>
          <div className="whitespace-pre-wrap text-success">{detail.after}</div>
        </div>
      ) : (
        <div className="whitespace-pre-wrap text-sm leading-relaxed">{detail.after}</div>
      )}
      {detail.hint ? <div className="text-xs text-muted">{detail.hint}</div> : null}
      <div className="text-xs text-muted">{undo}</div>
      {!item.provisional && (
        <Row className="justify-end">
          <Button
            size="sm"
            variant="danger-soft"
            isDisabled={busy || !canUndo}
            onPress={onReject}
          >
            不同意
          </Button>
          <Button size="sm" variant="primary" isDisabled={busy} onPress={onAccept}>
            {busy && <InlineSpinner className="size-3" />}
            同意
          </Button>
        </Row>
      )}
    </div>
  )
}

/** 一条结构调整的展示文案与「不同意」的后果。 */
function structureText(item: Exclude<ReviewItem, { kind: 'edit' } | { kind: 'recompose' }>): {
  tag: string
  detail: { id: string; before: string | null; after: string; hint?: string }
  undo: string
  canUndo: boolean
} {
  switch (item.kind) {
    case 'move': {
      const m = item.move
      // A grouped entry stands for a whole destination chapter in a source
      // that had no chapters at all; there is no single paragraph to put
      // back, so it must not offer a restore it cannot perform.
      const grouped = m.count !== undefined && m.count > 0
      return {
        tag: `段落 · ${MOVE_LABEL[m.kind] ?? m.kind}`,
        detail: {
          id: m.node_id,
          before: m.from,
          after: m.to,
          hint: grouped
            ? `原文没有分章，这一章收进了 ${m.count} 段原文。`
            : m.text,
        },
        undo: grouped
          ? '原文本来没有章节，无法逐段还原；如不同意，请在「结构方案」里手动调整。'
          : m.kind === 'to_element'
            ? '不同意：解除要素绑定，这段回到正文。'
            : `不同意：按原文顺序把这段放回「${m.from}」。`,
        canUndo: !grouped,
      }
    }
    case 'section': {
      const c = item.change
      const retitle = c.kind === 'retitled'
      const level = c.kind === 'releveled'
      const reparent = c.kind === 'reparented'
      // A releveled card may also carry a parent change: one structural act
      // can move a chapter both deeper and under a different chapter. Show
      // both facts, and undo both together, or the restored tree would keep
      // a parent that no longer matches the restored level.
      const parentAlsoMoved =
        level &&
        (c.baseline_parent_heading_node_id ?? '') !== (c.parent_heading_node_id ?? '')
      const before = retitle
        ? (c.baseline_title ?? '')
        : level
          ? shapeLabel(c.baseline_level, c.baseline_parent, parentAlsoMoved)
          : reparent
            ? (c.baseline_parent ?? '顶层')
            : null
      const after = retitle
        ? c.title
        : level
          ? shapeLabel(c.level, c.parent, parentAlsoMoved)
          : reparent
            ? (c.parent ?? '顶层')
            : c.title
      return {
        tag: SECTION_LABEL[c.kind] ?? c.kind,
        detail: {
          id: c.id,
          before,
          after,
          hint:
            c.kind === 'missing'
              ? '原文有这一章，模型（或当前方案）没有保留它。'
              : c.kind === 'synthesized'
                ? '这个标题不是原文里的话，是模型新写的。'
                : c.kind === 'added'
                  ? '这一章的标题段落原文本来存在，但原文自身没有把它当作章节起点。'
                  : reparent
                    ? `「${c.title}」换到了上一级，章内文字没变。`
                    : parentAlsoMoved
                      ? `「${c.title}」的层级和上级同时变了，章内文字没变。`
                      : undefined,
        },
        undo:
          retitle
            ? `不同意：把标题改回「${c.baseline_title ?? ''}」。`
            : level
              ? parentAlsoMoved
                ? `不同意：把层级和上级一起改回${levelLabel(c.baseline_level)}、挂回「${c.baseline_parent ?? '顶层'}」。`
                : `不同意：把层级改回${levelLabel(c.baseline_level)}。`
              : reparent
                ? `不同意：把这一章挂回「${c.baseline_parent ?? '顶层'}」。`
                : c.kind === 'missing'
                  ? '不同意：恢复这一章，并按原文顺序收回它的正文。'
                  : c.kind === 'synthesized' || c.kind === 'added'
                    ? '不同意：取消这一章，正文并入相邻章节。'
                    : '不同意：需要手动调整。',
        canUndo:
          retitle || level
            ? true
            : reparent
              ? true
              : c.kind === 'missing'
                ? (c.heading_node_id ?? '') !== ''
                : c.kind === 'synthesized' || c.kind === 'added',
      }
    }
    case 'section-move': {
      const s = item.move
      return {
        tag: '章节换了位置',
        detail: {
          id: s.id,
          before: `原文第 ${s.baseline_pos} 章`,
          after: `成品第 ${s.rendered_pos} 章`,
          hint: s.title,
        },
        undo: '不同意：整章按原文顺序排回原位置。',
        canUndo: true,
      }
    }
    case 'element': {
      const e = item.binding
      if (e.kind === 'unbound') {
        return {
          tag: `段落 · 不再是${roleLabel(e.role)}`,
          detail: {
            id: e.node_id,
            before: `公文要素：${roleLabel(e.role)}`,
            after: '普通正文',
            hint: e.text,
          },
          undo: `不同意：把这段重新绑定为${roleLabel(e.role)}。`,
          canUndo: true,
        }
      }
      return {
        tag: `段落 · 改作${roleLabel(e.role)}`,
        detail: { id: e.node_id, before: '正文', after: `公文要素：${roleLabel(e.role)}`, hint: e.text },
        undo: '不同意：解除要素绑定，这段回到正文。',
        canUndo: true,
      }
    }
    case 'meta':
      return {
        tag: `模型自拟${item.change.label}`,
        detail: {
          id: '',
          before: null,
          after: item.change.value,
          hint: '原文没有这段文字，是模型按公文体例补的。',
        },
        undo: `不同意：清空模型写的${item.change.label}，成品里不再出现。`,
        canUndo: true,
      }
    case 'body-order':
      return {
        tag: item.order.kind === 'preamble' ? '前言段序被调整' : '章节内段序被调整',
        detail: {
          id: item.order.section_id ?? '',
          before: '原文顺序',
          after: '成品顺序不同',
          hint: item.order.title,
        },
        undo: item.order.has_parts
          ? '这一节含整段重组，段序由重组决定；要还原请先处理对应的重组项。'
          : '不同意：按原文顺序把这一节（或前言）重新排回。',
        canUndo: !item.order.has_parts,
      }
  }
}

/** 公文要素的中文名；与后端 roleNames 一致。 */
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
  }
  return map[role] ?? role
}

/** 「层级 + 上级」的展示文案；上级没有一起变时只显示层级。 */
function shapeLabel(level: number | undefined, parent: string | undefined, withParent: boolean): string {
  const lv = levelLabel(level)
  return withParent ? `${lv}，上级「${parent ?? '顶层'}」` : lv
}

/** 「不同意」一条结构调整：按类型还原到原文。 */
function revertStructure(
  item: Exclude<ReviewItem, { kind: 'edit' } | { kind: 'recompose' }>,
): void {
  switch (item.kind) {
    case 'move':
      if ((item.move.count ?? 0) > 0) break
      if (item.move.kind === 'to_element') restoreElementToBody(item.move.node_id)
      else restoreMoveTarget(item.move.node_id, item.move.from_id)
      break
    case 'section':
      if (item.change.kind === 'retitled') {
        restoreSectionTitle(item.change.id, item.change.baseline_title ?? '')
      } else if (item.change.kind === 'releveled') {
        if (
          (item.change.baseline_parent_heading_node_id ?? '') !==
          (item.change.parent_heading_node_id ?? '')
        ) {
          restoreSectionShape(
            item.change.id,
            item.change.baseline_level ?? 1,
            item.change.baseline_parent_heading_node_id ?? '',
          )
        } else {
          restoreSectionLevel(item.change.id, item.change.baseline_level ?? 1)
        }
      } else if (item.change.kind === 'reparented') {
        restoreSectionParent(
          item.change.id,
          item.change.baseline_parent_heading_node_id ?? '',
        )
      } else if (item.change.kind === 'missing') {
        restoreMissingSection(item.change)
      } else {
        const done =
          (item.change.heading_node_id ?? '') !== ''
            ? revertAddedSectionToBody(item.change.id)
            : dissolveSynthesizedSection(item.change.id)
        if (!done) return
      }
      break
    case 'section-move':
      restoreSectionOrder(item.move.id)
      break
    case 'element':
      if (item.binding.kind === 'unbound') {
        restoreElementBinding(item.binding.node_id, item.binding.role)
      } else {
        restoreElementToBody(item.binding.node_id)
      }
      break
    case 'meta':
      restoreMetaField(item.change.field as keyof Meta, item.change.label)
      break
    case 'body-order':
      if (item.order.kind === 'preamble') restorePreambleOrder()
      else restoreBodyOrder(item.order.section_id ?? '')
      break
  }
}

/**
 * 整段重组卡片。
 *
 * 左侧是原文，右侧是模型拆出的新段落；只显示差异最大的事实：一段变几段、
 * 拆后每段是什么。理由放在末尾，不额外加说明标题。
 */
function RecomposeCard({
  rec,
  provisional,
  busy,
  onAccept,
  onReject,
}: {
  rec: Recompose
  provisional: boolean
  busy: boolean
  onAccept: () => void
  onReject: () => void
}) {
  return (
    <div className="space-y-2 rounded-2xl border border-border bg-surface p-3">
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <Badge color={provisional ? 'accent' : 'warning'}>
          {provisional ? '生成中' : '整段重组'}
        </Badge>
        <span className="text-muted">1 段 → {rec.parts.length} 段</span>
        <span className="font-mono text-muted">{rec.node_id}</span>
      </div>
      <div className="space-y-1.5 text-sm leading-relaxed">
        {rec.parts.map((part, index) => {
          const role = rec.roles?.[index] ?? ''
          return (
            <p key={index} className="whitespace-pre-wrap text-success">
              {role === 'recipient' || role === 'preamble' ? (
                <Badge color="accent" className="mr-2 align-middle">
                  {role === 'recipient' ? '主送机关' : '正文前言'}
                </Badge>
              ) : null}
              {part}
            </p>
          )
        })}
      </div>
      {rec.reason ? <div className="text-xs text-muted">{rec.reason}</div> : null}
      {!provisional && (
        <Row className="justify-end">
          <Button size="sm" variant="danger-soft" isDisabled={busy} onPress={onReject}>
            不同意
          </Button>
          <Button size="sm" variant="primary" isDisabled={busy} onPress={onAccept}>
            {busy && <InlineSpinner className="size-3" />}
            同意
          </Button>
        </Row>
      )}
    </div>
  )
}

/** 处理完一条校对项后的结果句，避免卡片消失得没有交代。 */
function resultText(item: AppliedEdit): string {
  const after = item.edit.after.trim()
  const target = after === '' ? '' : `「${after.length > 18 ? `${after.slice(0, 18)}…` : after}」`
  return item.accepted
    ? `已覆盖原文${target ? `：${target}` : ''}`
    : `已保留原文${target ? `，未采用：${target}` : ''}`
}

/** 处理完一条重组后的结果句。 */
function recomposeResultText(rec: Recompose, accepted: boolean): string {
  return accepted
    ? `已按重组覆盖原文：1 段拆为 ${rec.parts.length} 段`
    : '已保留原文，未采用重组'
}

/** 右侧对话：要求会变成待审改动，不会直接覆盖原文。 */
function ChatPanel({
  selection,
  onClearSelection,
}: {
  selection: SelectionMatch | null
  onClearSelection: () => void
}) {
  useGov()
  const [text, setText] = useState('')
  const endRef = useRef<HTMLDivElement | null>(null)
  const hasDocument = (gov.upload?.doc_id ?? '') !== ''

  useEffect(() => {
    endRef.current?.scrollIntoView({ block: 'end' })
  }, [gov.chat.length, gov.chatting])

  function send(): void {
    if (text.trim() === '') return
    void requestRevision(
      text,
      selection ? { text: selection.text, nodeId: selection.nodeId } : undefined,
    )
    setText('')
    onClearSelection()
  }

  const pill =
    selection === null
      ? ''
      : selection.text.length > 10
        ? `${selection.text.slice(0, 10)}…`
        : selection.text

  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      {gov.chat.length > 0 && (
        <div className="min-h-0 flex-1 space-y-2 overflow-auto pr-1">
          {gov.chat.map((m) => (
            <div
              key={m.id}
              className={cn(
                'max-w-[92%] whitespace-pre-wrap rounded-2xl px-3 py-2 text-sm leading-relaxed',
                m.role === 'user'
                  ? 'ml-auto bg-accent text-accent-foreground'
                  : 'bg-surface-secondary text-foreground',
              )}
            >
              {m.text}
            </div>
          ))}
          {gov.chatting && (
            <div className="flex items-center gap-2 text-xs text-muted">
              <InlineSpinner className="size-3" />
              正在生成改动…
            </div>
          )}
          <div ref={endRef} />
        </div>
      )}

      <div className="rounded-2xl border border-border bg-surface p-2">
        {selection && (
          <div className="mb-1.5 flex items-center gap-1.5">
            <span
              className="max-w-full truncate rounded-full bg-accent-soft px-2.5 py-1 text-xs text-accent-soft-foreground"
              title={selection.text}
            >
              {pill}
            </span>
            <button
              type="button"
              className="text-xs text-muted hover:text-foreground"
              onClick={onClearSelection}
              aria-label="移除选中文字"
            >
              ✕
            </button>
          </div>
        )}
        <TextArea
          rows={2}
          placeholder="提出修改要求…"
          value={text}
          disabled={!hasDocument}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              send()
            }
          }}
          className="min-h-16 resize-none border-0 bg-transparent shadow-none"
        />
        <div className="flex justify-end">
          <Button
            size="sm"
            variant="primary"
            isDisabled={!hasDocument || gov.chatting || text.trim() === ''}
            onPress={send}
          >
            {gov.chatting ? <InlineSpinner className="size-3" /> : null}
            发送
          </Button>
        </div>
      </div>
    </div>
  )
}

/** 标题级别在预览里的颜色，与结构栏保持同一套语义。 */
const HEADING_STYLE: Record<number, { bg: string; bar: string }> = {
  1: { bg: 'rgba(88, 101, 242, 0.10)', bar: '#5865f2' },
  2: { bg: 'rgba(196, 138, 0, 0.12)', bar: '#c48a00' },
  3: { bg: 'rgba(29, 138, 76, 0.10)', bar: '#1d8a4c' },
}

/**
 * 把标题标记写进预览 iframe。
 *
 * 预览 HTML 由服务端生成，这里只加两类**视觉**标记：块背景/左侧色条，
 * 以及一个小号 H1/H2/H3 标签。标签用 absolute 定位，色条用 outline，
 * 两者都不参与布局，因此分页与纸张度量不受影响。
 */
function markHeadings(
  frame: HTMLIFrameElement,
  outline: OutlineItem[],
  enabled: boolean,
): void {
  const doc = frame.contentDocument
  if (!doc) return

  for (const el of doc.querySelectorAll<HTMLElement>('[data-gd-heading-mark]')) {
    el.removeAttribute('data-gd-heading-mark')
    el.style.removeProperty('background-color')
    el.style.removeProperty('outline')
    el.style.removeProperty('outline-offset')
    el.style.removeProperty('border-radius')
    if (el.dataset.gdHeadingPosition === 'relative') {
      el.style.removeProperty('position')
      delete el.dataset.gdHeadingPosition
    }
  }
  for (const tag of doc.querySelectorAll<HTMLElement>('[data-gd-heading-tag]')) tag.remove()

  if (!enabled) return

  for (const item of outline) {
    if (item.nodeId === '') continue
    const el = doc.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(item.nodeId)}"]`)
    if (!el) continue
    const style = HEADING_STYLE[item.level] ?? HEADING_STYLE[3]
    el.setAttribute('data-gd-heading-mark', String(item.level))
    el.style.backgroundColor = style.bg
    el.style.outline = `2px solid ${style.bar}`
    el.style.outlineOffset = '0'
    el.style.borderRadius = '2px'

    const tag = doc.createElement('span')
    tag.setAttribute('data-gd-heading-tag', String(item.level))
    tag.textContent = headingTag(item.level)
    tag.style.cssText = [
      'position:absolute',
      'right:0',
      'top:0',
      'padding:0 3px',
      'border-radius:3px',
      `background:${style.bar}`,
      'color:#fff',
      'font:600 9px/1.5 system-ui,sans-serif',
      'letter-spacing:0',
      'pointer-events:none',
    ].join(';')
    if (getComputedStyle(el).position === 'static') el.style.position = 'relative'
    el.dataset.gdHeadingPosition = 'relative'
    el.appendChild(tag)
  }
}

/**
 * 在预览里定位到某个标题。
 *
 * 滚动必须发生在 iframe **自己的文档**里：纸张在一个独立滚动上下文中（缩放后
 * 高度是 1123px × scale），滚动外层的预览容器到不了页内更靠后的标题。
 * scrollIntoView 的 behavior 用默认值而不是 smooth：跨文档的平滑滚动在
 * Chromium 里会被缩短成一次跳变，反而不如直接定位稳定。
 */
function jumpToHeading(frame: HTMLIFrameElement | null, item: OutlineItem): void {
  const doc = frame?.contentDocument
  if (!doc || item.nodeId === '') return
  const el = doc.querySelector<HTMLElement>(`[data-node-id="${CSS.escape(item.nodeId)}"]`)
  if (!el) return

  // 多页文档比 iframe 的 1123px 视口高，滚动发生在 iframe 自己的文档里。
  // sandbox 只禁用帧内脚本执行，父页面调用 DOM 滚动仍然有效，这正是把
  // 结构栏和纸张放在同源 iframe 里的原因。若将来改成一页式外滚动，下面的
  // 外层分支仍能工作。
  const win = frame?.contentWindow
  const scroller = doc.scrollingElement ?? doc.documentElement
  if (win && scroller && scroller.scrollHeight > win.innerHeight + 1) {
    const top = el.getBoundingClientRect().top + scroller.scrollTop - 16
    win.scrollTo({ top: Math.max(0, top), behavior: 'smooth' })
  } else {
    const stage = frame?.closest('.gd-paper-scaler')?.parentElement as HTMLElement | null
    if (stage) {
      const scaler = frame?.closest('.gd-paper-scaler') as HTMLElement | null
      const scale = scaler && scaler.offsetWidth > 0 ? scaler.offsetWidth / 794 : 1
      stage.scrollTo({ top: Math.max(0, el.offsetTop * scale - 16), behavior: 'smooth' })
    }
  }

  // 目标标题闪一下，用户才知道跳到了哪里。用 outline 而不是背景色：
  // 背景色要复原成「标记开/关」对应的颜色，恢复逻辑会和 markHeadings 打架。
  const previous = el.style.outline
  el.style.outline = '3px solid #5865f2'
  window.setTimeout(() => {
    el.style.outline = previous
  }, 1200)
}

/** 工作台本体。 */
export default function Workbench() {
  useGov()

  const docId = gov.upload?.doc_id ?? ''
  const [highlight, setHighlight] = useState(true)
  const [candidate, setCandidate] = useState<SelectionMatch | null>(null)
  const [attached, setAttached] = useState<SelectionMatch | null>(null)
  const [showAdd, setShowAdd] = useState(false)
  const frameRef = useRef<HTMLIFrameElement | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    if (docId !== '') void loadSource(docId)
  }, [docId])

  const nodes = useMemo<NodeSummary[]>(
    () => gov.source?.doc.nodes ?? gov.upload?.nodes ?? [],
    [gov.source, gov.upload],
  )

  const outline = useMemo(() => {
    // 模型生成过程中，先显示已经闭合的章节。baseline 方案此刻虽然可用，
    // 但它代表的是“原文结构”，不是用户正在等待的模型结果。
    if (gov.analyzing) {
      const fromPartial = outlineFromPlan(gov.partialPlan, nodes)
      if (fromPartial.length > 0) return fromPartial
    }
    const fromPlan = outlineFromPlan(gov.plan, nodes)
    if (fromPlan.length > 0) return fromPlan
    return outlineFromNodes(nodes)
  }, [gov.plan, gov.partialPlan, nodes])

  const attachFrame = useCallback(
    (frame: HTMLIFrameElement): void => {
      frameRef.current = frame
      markHeadings(frame, outline, highlight)

      const doc = frame.contentDocument
      if (!doc) return
      const onSelection = (): void => {
        const match = readFrameSelection(frame, nodes)
        if (!match) {
          setShowAdd(false)
          return
        }
        setCandidate(match)
        setShowAdd(true)
      }
      doc.addEventListener('mouseup', onSelection)
      doc.addEventListener('keyup', onSelection)
    },
    [outline, highlight, nodes],
  )

  useEffect(() => {
    if (frameRef.current) markHeadings(frameRef.current, outline, highlight)
  }, [outline, highlight])

  async function analyze(): Promise<void> {
    await reanalyze()
  }

  /** 模型不可用或不接受任务时，至少按原文自带结构给出一份可预览的初稿。 */
  async function previewBaseline(): Promise<void> {
    if (docId === '') return
    await startAnalysis(docId, '', undefined, undefined, true)
  }

  async function upload(file: File | undefined): Promise<void> {
    if (!file) return
    const ok = await uploadDocument(file, gov.selectedProfile)
    if (!ok || !gov.upload) return

    const id = gov.upload.doc_id
    void loadSource(id)

    // 上传后只按原文结构 + 当前排版规则出稿，立刻可看可下载。
    //
    // 大多数上传（招考公告、已有编号的通知）本地规则就够了，之前每次
    // 都顺手跑一遍模型，既慢又会在用户没有要求时改动文字与章节归属。
    // 需要 AI 时由用户点「AI 处理」，见 aiProcess()。
    await prepareDocument(id)
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-3 md:overflow-hidden md:px-5">
      <input
        ref={inputRef}
        className="sr-only"
        type="file"
        accept=".docx,.txt,.md"
        aria-label="选择源文件"
        onChange={(e) => {
          void upload(e.target.files?.[0])
          e.target.value = ''
        }}
      />

      {gov.analyzing && <IndeterminateProgress label="正在分析文档结构" />}

      {/* 已经有一份可用的原文结构时，模型失败不该把左栏清空；但也不能
          悄悄吞掉失败，否则用户会以为看到的方案就是模型结论。 */}
      {!gov.analyzing && gov.analysisError && gov.plan && (
        <div className="flex items-start gap-2 rounded-xl border border-warning/40 bg-warning-soft px-3 py-2 text-xs leading-relaxed">
          <span
            className="min-w-0 flex-1 whitespace-pre-wrap line-clamp-2"
            title={gov.analysisError}
          >
            {gov.analysisError}
          </span>
          <Btn size="sm" variant="secondary" onPress={() => void analyze()}>
            重试
          </Btn>
        </div>
      )}

      {/* 平板尺寸（≈700px）也应该并排：只把结构栏改成上方横排，左右两栏
          从 md 起就保留。再窄才上下堆叠。 */}
      <div className="flex min-h-0 flex-1 flex-col gap-3 md:flex-row">
        {/* 左半：浮动结构栏 + 预览 */}
        <section className="relative flex min-h-[60vh] min-w-0 flex-1 flex-col rounded-2xl bg-surface-secondary p-3 md:min-h-0">
          {/* 结构栏浮在预览左侧：absolute 不挤压纸张；中等宽度下改为顶部横排，
              避免窄栏里同时塞两张卡片。 */}
          <div className="relative z-20 mb-3 xl:absolute xl:left-3 xl:top-3 xl:mb-0">
            <OutlineRail
              outline={outline}
              highlight={highlight}
              onHighlight={setHighlight}
              onJump={(item) => jumpToHeading(frameRef.current, item)}
            />
          </div>
          <div className="min-h-0 min-w-0 flex-1 overflow-auto xl:pl-[16.5rem]">
            {/* 方案就绪前 /preview 没有内容可给；先显示状态，不要挂一个会
                把 404 JSON 画出来的 iframe。 */}
            {docId === '' ? (
              <PreviewUpload
                uploading={gov.uploading}
                onPick={() => inputRef.current?.click()}
                onDrop={(file) => void upload(file)}
              />
            ) : gov.plan ? (
              <PreviewPane compact height="100%" initialZoom={0.75} onReady={attachFrame} />
            ) : (
              <div className="flex h-full min-h-[24rem] flex-col items-center justify-center gap-3 rounded-2xl border border-border bg-surface px-6 py-10 text-center">
                {gov.analysisError ? (
                  <>
                    <span className="text-sm font-semibold text-danger">无法生成文档结构</span>
                    <p className="max-w-md whitespace-pre-wrap text-xs leading-relaxed text-muted">
                      {gov.analysisError}
                    </p>
                    <Row className="justify-center">
                      <Btn
                        size="sm"
                        variant="secondary"
                        isDisabled={gov.analyzing}
                        onPress={() => void analyze()}
                      >
                        重试
                      </Btn>
                      <Btn
                        size="sm"
                        variant="ghost"
                        isDisabled={gov.analyzing}
                        onPress={() => void previewBaseline()}
                      >
                        按原文结构预览
                      </Btn>
                    </Row>
                  </>
                ) : (
                  <>
                    <InlineSpinner className="size-5" />
                    <span className="text-sm font-medium">正在生成文档结构…</span>
                  </>
                )}
              </div>
            )}
          </div>

          {showAdd && candidate && (
            <button
              type="button"
              onClick={() => {
                setAttached(candidate)
                setShowAdd(false)
              }}
              className="absolute bottom-6 left-1/2 z-20 -translate-x-1/2 rounded-full bg-accent px-4 py-2 text-sm font-medium text-accent-foreground shadow-lg"
            >
              添加到输入框
            </button>
          )}
        </section>

        {/* 右半：5/6 校对 + 1/6 对话 */}
        {/*
          用 grid 的 5fr/1fr，而不是 flex-[5]/flex-[1]：flex 的可用高度会先被
          两张卡片的内边距和间距吃掉，实测外框只剩 4.4:1；grid 的两条轨道直接
          按整块高度分配，卡片再在轨道内加内边距，比值就是用户看到的 5:1。
        */}
        <section className="grid min-h-[78vh] min-w-0 flex-1 grid-rows-[minmax(0,5fr)_minmax(0,1fr)] gap-3 md:min-h-0">
          <div
            data-panel="review"
            className="flex min-h-0 flex-col rounded-2xl border border-border bg-surface p-3"
          >
            <div className="mb-2 flex items-center justify-between gap-2">
              <span className="text-sm font-semibold">校对项</span>
            </div>
            <div className="min-h-0 flex-1">
              <ReviewList />
            </div>
          </div>
          <div
            data-panel="chat"
            className="flex min-h-0 flex-col overflow-hidden rounded-2xl border border-border bg-surface p-3"
          >
            <ChatPanel
              selection={attached}
              onClearSelection={() => {
                setAttached(null)
                setCandidate(null)
                setShowAdd(false)
              }}
            />
          </div>
        </section>
      </div>
    </div>
  )
}
