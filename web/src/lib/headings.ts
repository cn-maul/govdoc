/**
 * 文档结构（最多三级标题）的提取。
 *
 * 结构栏和“标题高亮”必须看同一份数据：前者显示章节树，后者在预览上给
 * H1/H2/H3 着色。若两处各算一套，用户会看到树里有三级标题、预览却没标。
 *
 * 结构来源优先用方案：模型已经判定过的层级比原文样式更准；没有方案时才
 * 退回原文 heading 节点的 level。方案里超过三级的章节被截断，但不会丢。
 */
import type { NodeSummary, Plan, Section } from '../api/types'

export interface OutlineItem {
  /** 稳定键：章节 id 或原文节点 id。 */
  key: string
  /** 标题文本。 */
  title: string
  /** 1–3，用于预览标记 H1/H2/H3。 */
  level: number
  /** 对应原文节点，预览据此加标记。 */
  nodeId: string
}

/** 从方案提取前三级章节标题。 */
export function outlineFromPlan(
  plan: Plan | null | undefined,
  nodes: NodeSummary[] = [],
): OutlineItem[] {
  if (!plan) return []
  // 模型有时只填 title、不填 heading_node_id。结构栏若要可点击，就必须把
  // 标题文字映射回原文节点；这里只接受**完全一致**的标题文本，避免把普通
  // 正文误当成跳转目标。
  const exact = new Map<string, string>()
  for (const n of nodes) {
    const text = (n.text ?? '').trim()
    if (text !== '' && !exact.has(text)) exact.set(text, n.id)
  }
  const out: OutlineItem[] = []
  const walk = (sections: Section[], depth: number): void => {
    for (const s of sections) {
      const level = Math.max(1, Math.min(3, depth + 1))
      const title = (s.title ?? '').trim()
      if (title !== '') {
        const declared = (s.heading_node_id ?? '').trim()
        out.push({
          key: s.id,
          title,
          level,
          nodeId: declared !== '' ? declared : (exact.get(title) ?? ''),
        })
      }
      // 超过三级仍继续下探没有意义：界面只承诺最多三级。
      if (depth + 1 < 3) walk(s.children ?? [], depth + 1)
    }
  }
  walk(plan.sections ?? [], 0)
  return out
}

/**
 * 方案尚未生成时，用解析器已经识别出的标题占位。
 *
 * 这不是模型判断结果：它只来自原文自己的 Heading 样式或层次序数，因此
 * 不会凭空造标题；等方案到达后会自动切换到更准确的章节树。
 */
export function outlineFromNodes(nodes: NodeSummary[]): OutlineItem[] {
  const out: OutlineItem[] = []
  for (const n of nodes) {
    if (n.kind !== 'heading') continue
    const title = (n.text ?? '').trim()
    if (title === '') continue
    const level = Math.max(1, Math.min(3, n.level ?? 1))
    out.push({ key: n.id, title, level, nodeId: n.id })
  }
  return out
}

/** 标题层级对应的 H 标记。 */
export function headingTag(level: number): string {
  return `H${Math.max(1, Math.min(3, level))}`
}
