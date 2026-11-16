/**
 * 纸张预览（可嵌入）。
 *
 * 预览的 HTML 由 internal/render/preview 生成（服务端渲染），这里只负责把它
 * 装进 iframe。用 iframe 而不是 dangerouslySetInnerHTML 有三个理由：
 *   1. 预览自带 <style>，直接内联会污染整个应用的样式；
 *   2. iframe 的 sandbox 可以彻底禁止脚本，而预览内容包含用户上传的原文；
 *   3. 纸张尺寸（A4 210×297mm）与页面滚动由独立的文档上下文承担，缩放更可靠。
 *
 * 界面上的缩放只改变 iframe 的 CSS transform，不改纸张内部的国标度量。
 *
 * 这个组件同时被「校对工作台」的左栏（compact）与「全屏预览」页复用：两处的
 * 纸张必须一模一样，否则用户在校对时看到的和最终打印出来的不是同一张纸。
 */
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Button, ToggleButton, ToggleButtonGroup } from '@heroui/react'
import { api } from '../api/client'
import type { Section } from '../api/types'
import { gov, resetDocument } from '../store/govdoc'
import { useGov } from '../store/useGov'
import { AlertBox, LinkButton, Row } from '../ui/primitives'
import { cn } from '@heroui/react'

const ZOOMS = [0.5, 0.75, 1, 1.25, 1.5]

/**
 * A4 纸在 96dpi 下的尺寸（210×297mm），与 `.gd-paper-frame` 的 CSS 一致。
 * 缩放后的占位盒按它算像素，滚动区域才和肉眼看到的一样大。
 */
const PAPER_WIDTH_PX = 794
const PAPER_HEIGHT_PX = 1123

/** 收集所有章节的 body_parts，作为预览缓存键的一部分。 */
function collectBodyParts(sections: Section[]): unknown[] {
  const out: unknown[] = []
  const walk = (list: Section[]): void => {
    for (const section of list) {
      for (const bp of section.body_parts ?? []) out.push([section.id, bp.node_id, bp.part])
      if (section.children?.length) walk(section.children)
    }
  }
  walk(sections)
  return out
}

export interface PreviewPaneProps {
  /** 紧凑模式：工具栏只留缩放与刷新，卡片与说明由外层给。 */
  compact?: boolean
  /** 额外的工具栏内容（例如「全屏预览」入口）。 */
  toolbarExtra?: ReactNode
  /**
   * 滚动视口的高度（纸张在其中居中，超出则滚动）。
   * 不传则由纸张本身撑开整页 —— 全屏预览页要的就是这个。
   */
  height?: string
  /** 预览加载完成。工作台据此注入标题标记与绑定选区监听。 */
  onReady?: (frame: HTMLIFrameElement) => void
  /** 初始缩放。工作台的左半栏比整页窄，默认 75% 更易读。 */
  initialZoom?: number
}

export default function PreviewPane({
  compact = false,
  toolbarExtra,
  height,
  onReady,
  initialZoom,
}: PreviewPaneProps) {
  useGov()

  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState('')
  /**
   * 缩放。紧凑模式（校对左栏）默认 75%：A4 纸固定 794px 宽，100% 时要横向
   * 滚动才能看全整行，75% 兼顾「整页可见」与正文可读。需要细看时再点大——
   * 缩放组仍然可点，不锁死。
   */
  const [zoom, setZoom] = useState(initialZoom ?? (compact ? 0.75 : 1))
  /** 预览的缓存键：方案每次改动后都要重新拉，否则看到的还是旧纸。 */
  const [revision, setRevision] = useState(0)

  /**
   * 三个信号都会让纸张过期：重新成文、重新校验、改动声明变化。
   *
   * 用 ref 跳过首次取值，否则挂载那一刻就会白白多拉一次。
   */
  const seenRender = useRef(false)
  useEffect(() => {
    if (!seenRender.current) {
      seenRender.current = true
      return
    }
    setRevision((r) => r + 1)
  }, [gov.render])

  const seenReport = useRef(false)
  useEffect(() => {
    if (!seenReport.current) {
      seenReport.current = true
      return
    }
    setRevision((r) => r + 1)
  }, [gov.report])

  // 方案里的内容改动一改（文字改动或整段重组），预览就必须重新拉。只监听
  // edits 会漏掉重组：同意重组后 recompose 与 body_parts 变了，但纸张仍是
  // 同意前的原文。
  const contentSignature = JSON.stringify({
    edits: gov.plan?.edits ?? [],
    recompose: gov.plan?.recompose ?? [],
    parts: collectBodyParts(gov.plan?.sections ?? []),
  })
  const seenEdits = useRef(false)
  useEffect(() => {
    if (!seenEdits.current) {
      seenEdits.current = true
      return
    }
    setRevision((r) => r + 1)
  }, [contentSignature])

  const docId = gov.upload?.doc_id ?? ''
  const src = docId === '' ? '' : `${api.previewURL(docId)}?v=${revision}`
  const previewURL = docId === '' ? '' : api.previewURL(docId)

  // 预览地址变化（方案刷新、换文档）时，上一次的错误已经不代表当前状态。
  // 必须放在提前 return 之前，否则条件分支会改变 Hook 的调用顺序。
  useEffect(() => {
    setFailed('')
  }, [src])

  function reload(): void {
    setRevision((r) => r + 1)
    setLoading(true)
    setFailed('')
  }

  if (docId === '') {
    return (
      <div className="rounded-xl border border-dashed border-border px-4 py-10 text-center text-sm text-muted">
        还没有可预览的文档。
      </div>
    )
  }

  return (
    <div className="flex min-w-0 flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Row className="gap-1">
          <span className="text-xs text-muted">缩放</span>
          <ToggleButtonGroup
            aria-label="预览缩放"
            selectionMode="single"
            selectedKeys={[String(zoom)]}
            onSelectionChange={(keys) => {
              const next = Number([...keys][0])
              if (Number.isFinite(next)) setZoom(next)
            }}
          >
            {ZOOMS.map((z) => (
              <ToggleButton key={z} id={String(z)} size="sm" className="px-3">
                {Math.round(z * 100)}%
              </ToggleButton>
            ))}
          </ToggleButtonGroup>
        </Row>
        <Row className="gap-1">
          {toolbarExtra}
          <Button size="sm" variant="ghost" onPress={reload}>
            {loading && <span aria-hidden="true" className="inline-flex size-3 animate-spin rounded-full border-2 border-current border-t-transparent" />}
            刷新
          </Button>
          <LinkButton size="sm" variant="secondary" href={previewURL} target="_blank">
            新窗口
          </LinkButton>
        </Row>
      </div>

      {failed !== '' && (
        <AlertBox status="danger" title="预览加载失败">
          <div className="space-y-2">
            <div>{failed}</div>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="secondary" onPress={reload}>
                重试
              </Button>
              <Button size="sm" variant="ghost" onPress={resetDocument}>
                换文档
              </Button>
            </div>
          </div>
        </AlertBox>
      )}

      {/*
        缩放用 transform 实现，而 transform 不影响布局盒：外面这一层按「纸张
        缩放后的真实像素」占位，滚动区域才是对的。

        宽度必须写成像素（794px × 缩放），不能用百分比 —— 百分比是相对容器算的，
        50% 时会写成 200%，把盒子撑得比纸张宽一倍，凭空多出一条横向滚动条。
      */}
      {failed === '' && (
        <div
          className={cn('gd-paper-stage', compact && 'gd-paper-stage--compact')}
          style={height ? { height } : undefined}
        >
          <div
            className="gd-paper-scaler"
            style={{
              width: `${PAPER_WIDTH_PX * zoom}px`,
              height: `${PAPER_HEIGHT_PX * zoom}px`,
            }}
          >
            <iframe
              className="gd-paper-frame"
              src={src}
              style={{ transform: `scale(${zoom})`, transformOrigin: 'top left' }}
              title="公文分页预览"
              sandbox="allow-same-origin"
              referrerPolicy="no-referrer"
              onLoad={(e) => {
                setLoading(false)
                // 服务端的错误响应同样是 200 以外的 JSON，iframe 里没有
                // .gd-root 就说明拿到的不是纸张。把错误转成界面提示，而不是
                // 让用户对着一屏 JSON 猜发生了什么。
                const message = previewErrorMessage(e.currentTarget)
                if (message !== '') {
                  setFailed(message)
                  return
                }
                setFailed('')
                onReady?.(e.currentTarget)
              }}
              onError={() => {
                setLoading(false)
                setFailed('无法加载预览。请确认后端服务正在运行，且文档仍然存在于服务端。')
              }}
            />
          </div>
        </div>
      )}

    </div>
  )
}

/**
 * 判断 iframe 里装的是不是真正的预览。
 *
 * 服务端的 /preview 在方案未生成时返回 JSON 错误，浏览器会把它排在纸张
 * 位置里。正常预览一定带 .gd-root，没有就读取错误体并转成中文提示。
 */
function previewErrorMessage(frame: HTMLIFrameElement): string {
  const doc = frame.contentDocument
  if (!doc) return ''
  if (doc.querySelector('.gd-root')) return ''

  const raw = (doc.body?.innerText ?? '').trim()
  if (raw === '') return '预览内容为空，请重新分析文档。'
  try {
    const payload = JSON.parse(raw) as { error?: { message?: string } }
    const message = payload?.error?.message
    if (typeof message === 'string' && message.trim() !== '') return message
  } catch {
    // 不是 JSON：多半是服务端返回了其他 HTML 页面，原样展示前先截断。
  }
  return raw.length > 240 ? `${raw.slice(0, 240)}…` : raw
}
