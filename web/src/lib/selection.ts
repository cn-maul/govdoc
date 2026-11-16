/**
 * 预览与原文的选中范围。
 *
 * iframe 里的预览是服务端生成的 HTML，用户用鼠标选中后只能拿到
 * `window.getSelection()` 的文本，拿不到它属于哪个 node。为了把“选中的几段”
 * 变成可校验的改动，这里把选中文字和原文节点做一次包含匹配：优先整段命中，
 * 其次选中最长的公共节点范围。
 *
 * 局限是刻意的：匹配不到时宁可不添加，也不猜一个节点。猜错会让 AI 改到
 * 用户没选的段落，而这正是改动清单要防止的事。
 */
import type { NodeSummary } from '../api/types'

export interface SelectionMatch {
  /** 选中的文字（逐字，含标点与换行）。 */
  text: string
  /** 命中一个节点时的节点 id。 */
  nodeId: string
  /** 命中多个连续节点时的节点 id。 */
  nodeIds: string[]
}

/** 把选中文字对应到原文节点。 */
export function matchSelection(
  nodes: NodeSummary[],
  raw: string,
  nodeId?: string,
): SelectionMatch | null {
  if (nodeId) {
    const node = nodes.find((n) => n.id === nodeId)
    if (!node) return null
    const text = trimGeneratedPrefix(raw, node.text ?? '')
    return text === '' ? null : { text, nodeId, nodeIds: [nodeId] }
  }

  const text = raw.replace(/\r\n/g, '\n').trim()
  if (text === '') return null

  const prose = nodes.filter((n) => n.kind !== 'page_break' && (n.text ?? '').includes(text))
  if (prose.length > 0) {
    // 同一段文字可能出现多次；优先最长的容器，避免选了一整段却匹配到只含
    // 其中一句话的另一段。
    prose.sort((a, b) => (b.text?.length ?? 0) - (a.text?.length ?? 0))
    return { text, nodeId: prose[0].id, nodeIds: [prose[0].id] }
  }

  // 跨段选择：按原文顺序找出所有被选中的节点。要求它们连续，否则界面上
  // “几段文字”的语义就不成立了。
  const hit = nodes
    .filter((n) => n.kind !== 'page_break' && (n.text ?? '').trim() !== '')
    .filter((n) => text.includes(n.text.trim()) || n.text.includes(firstLine(text)))
  if (hit.length === 0) return null
  const sorted = [...hit].sort((a, b) => a.order - b.order)
  const contiguous = sorted.every((n, i) => i === 0 || n.order === sorted[i - 1].order + 1)
  if (!contiguous) return null
  return {
    text,
    nodeId: sorted.length === 1 ? sorted[0].id : '',
    nodeIds: sorted.map((n) => n.id),
  }
}

/**
 * 从预览 iframe 读取选区。
 *
 * 不能只靠字符串包含来定位：组装后的预览会在真实标题前插入层次序数，
 * 选中文本因此可能带着原文里没有的「一、」。优先读 DOM 上的
 * `data-node-id`，再按该节点原文把生成的前缀去掉。
 */
export function readFrameSelection(
  frame: HTMLIFrameElement,
  nodes: NodeSummary[],
): SelectionMatch | null {
  const doc = frame.contentDocument
  const win = frame.contentWindow
  if (!doc || !win) return null

  const sel = win.getSelection()
  if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null
  const raw = sel.toString().replace(/\r\n/g, '\n').trim()
  if (raw === '') return null

  const range = sel.getRangeAt(0)
  const ids: string[] = []
  for (const el of doc.querySelectorAll<HTMLElement>('[data-node-id]')) {
    const id = el.dataset.nodeId ?? ''
    if (id === '' || ids.includes(id)) continue
    try {
      if (range.intersectsNode(el)) ids.push(id)
    } catch {
      // 某些浏览器在节点已脱离文档时会抛错；忽略这一个即可。
    }
  }

  if (ids.length === 1) {
    const node = nodes.find((n) => n.id === ids[0])
    const text = trimGeneratedPrefix(raw, node?.text ?? '')
    return text === '' ? null : { text, nodeId: ids[0], nodeIds: ids }
  }
  if (ids.length > 1) {
    return { text: raw, nodeId: '', nodeIds: ids }
  }
  return matchSelection(nodes, raw)
}

/**
 * 去掉预览自动生成的层次序数，让 selected text 与原文一致。
 *
 * 只从开头最多尝试 16 个字符：足够跨过「一、」「（一）」「1.」这类序号，
 * 又不会把用户真正选中的正文当成前缀吃掉。
 */
function trimGeneratedPrefix(raw: string, nodeText: string): string {
  const text = raw.trim()
  if (text === '') return ''
  if (nodeText.includes(text)) return text
  const limit = Math.min(16, text.length - 1)
  for (let cut = 1; cut <= limit; cut++) {
    const candidate = text.slice(cut).trimStart()
    if (candidate !== '' && nodeText.includes(candidate)) return candidate
  }
  return text
}

/** 选中文字的第一行，用于跨段匹配。 */
function firstLine(s: string): string {
  const line = s.split('\n')[0].trim()
  return line.length > 12 ? line.slice(0, 12) : line
}
