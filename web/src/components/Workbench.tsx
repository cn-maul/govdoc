/**
 * 单页工作台。
 *
 * 上传后不再分四步：左边是原文预览与结构，右边是待校对改动与对话。
 * 模型给出的任何文字修改都先进入校对栏；用户点「同意」才写回方案并刷新
 * 预览，点「不同意」则丢掉这条建议，原文保持不变。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Switch, cn } from '@heroui/react'
import { editLabel, type Edit, type NodeSummary } from '../api/types'
import { headingTag, outlineFromNodes, outlineFromPlan, type OutlineItem } from '../lib/headings'
import { readFrameSelection, type SelectionMatch } from '../lib/selection'
import {
  acceptDeclaredEdit,
  acceptPendingEdit,
  acceptRecompose,
  dismissPendingEdit,
  editSignature,
  gov,
  loadSource,
  notify,
  reanalyze,
  rejectDeclaredEdit,
  rejectRecompose,
  requestRevision,
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

/** 一条待审综合项：文字改动或整段重组。 */
type ReviewItem =
  | { kind: 'edit'; edit: Edit; pending: boolean; index: number; provisional: boolean }
  | {
      kind: 'recompose'
      rec: Recompose
      pending: PendingRecompose | null
      index: number
      provisional: boolean
    }

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

  const recompose: ReviewItem[] = gov.pendingRecompose.map((item, index) => ({
    kind: 'recompose',
    rec: item.recompose,
    pending: item,
    index,
    provisional: false,
  }))

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
  const items = gov.analyzing ? streaming : [...recompose, ...pending, ...declared]

  async function accept(item: ReviewItem): Promise<void> {
    const key = reviewKey(item)
    setBusy(key)
    if (item.kind === 'recompose') await acceptRecompose(item.index)
    else if (item.pending) await acceptPendingEdit(item.index)
    else await acceptDeclaredEdit(item.edit)
    setBusy('')
  }

  async function reject(item: ReviewItem): Promise<void> {
    const key = reviewKey(item)
    setBusy(key)
    if (item.kind === 'recompose') {
      rejectRecompose(item.index)
      notify('info', '已拒绝这项重组，原文保持不变。')
    } else if (item.pending) {
      dismissPendingEdit(item.index)
      notify('info', '已拒绝这项改动，原文保持不变。')
    } else {
      await rejectDeclaredEdit(item.edit)
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
  if (item.kind === 'recompose') {
    return `${item.provisional ? 'sr' : 'r'}-${item.rec.node_id}-${item.index}`
  }
  return `${item.provisional ? 'se' : item.pending ? 'pe' : 'de'}-${editSignature(item.edit)}-${item.index}`
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

    // 先按原文结构出一份可预览的初稿，模型配置好时再叠一轮语义分析。
    // 上传与工作台在同一个页面，用户不会看到「上传成功但左边一直空着」。
    const useModel = gov.health?.llm_configured ?? false
    void (async () => {
      await startAnalysis(id, '', undefined, undefined, true)
      if (!useModel) return
      await startAnalysis(id, '', undefined, gov.config?.allow_edits ?? true)
    })()
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
