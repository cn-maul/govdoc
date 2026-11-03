/**
 * 校验报告的派生视图。
 *
 * 报告本身是一串平铺的 diagnostics；校对区需要的是「按原文节点索引」的问题表，
 * 这样左栏可以给每个节点标出「漏收（红）/ 重复（黄）」，用户一眼看到要修哪几段。
 */

import type { Diagnostic, Report, Severity } from '../api/types'

/** 诊断码：与 internal/docmodel/verify.go 里的 Code 常量一一对应。 */
export const DiagCode = {
  unknownRef: 'unknown_ref',
  duplicate: 'duplicate_content',
  uncovered: 'uncovered_content',
  headingMismatch: 'heading_mismatch',
  badLevel: 'bad_level',
  reordered: 'reordered',
  synthesized: 'synthesized_heading',
  emptySection: 'empty_section',
  noPlan: 'no_plan',
  missingSectionID: 'missing_section_id',
} as const

/** 一个节点上挂的问题汇总。 */
export interface NodeIssue {
  /** 最严重的级别决定颜色。 */
  severity: Severity
  codes: string[]
  messages: string[]
}

const RANK: Record<Severity, number> = { error: 0, warning: 1, info: 2 }

/** 把 diagnostics 按 node_ids 索引；没有 node_ids 的（如结构问题）不进表。 */
export function issuesByNode(report: Report | null | undefined): Map<string, NodeIssue> {
  const out = new Map<string, NodeIssue>()
  // `diagnostics` 可能是 JSON null（Go 的 nil slice，校验通过时恰好为空）。
  for (const d of report?.diagnostics ?? []) {
    for (const id of d.node_ids ?? []) {
      const cur = out.get(id)
      if (!cur) {
        out.set(id, { severity: d.severity, codes: [d.code], messages: [d.message] })
        continue
      }
      cur.codes.push(d.code)
      cur.messages.push(d.message)
      if (RANK[d.severity] < RANK[cur.severity]) cur.severity = d.severity
    }
  }
  return out
}

/** 某个节点在校对区的状态标记。 */
export type NodeStatus = 'missing' | 'duplicate' | 'mismatch' | 'heading' | 'ok'

/**
 * 由问题表推出节点的展示状态。
 *
 * 优先级：漏收 > 重复 > 标题不符 > 正常。「漏收」最严重（内容丢失），
 * 其次是「重复」（同一段被两处引用）。heading_mismatch 单独成一类：它表示
 * 该节点被用作某章节标题、但章节标题文本与原文不同，修法是改回原文或
 * 解除标题关联，与「重复收录」不是一回事。
 */
export function nodeStatus(issue: NodeIssue | undefined): NodeStatus {
  if (!issue) return 'ok'
  if (issue.codes.includes(DiagCode.uncovered)) return 'missing'
  if (issue.codes.includes(DiagCode.duplicate)) return 'duplicate'
  if (issue.codes.includes(DiagCode.headingMismatch)) return 'mismatch'
  return 'ok'
}

/** 按严重级别筛选诊断。 */
export function diagnosticsOf(report: Report | null | undefined, sev: Severity): Diagnostic[] {
  return (report?.diagnostics ?? []).filter((d) => d.severity === sev)
}

/** 报告里所有问题的中文计数摘要，用于顶部概览。 */
export function issueCounts(report: Report | null | undefined): Record<Severity, number> {
  const out: Record<Severity, number> = { error: 0, warning: 0, info: 0 }
  for (const d of report?.diagnostics ?? []) out[d.severity]++
  return out
}

/**
 * 导出被禁用的中文原因，逐条列出 error 级诊断。
 * 「禁用但不说明为什么」是最容易让用户卡住的界面，所以这里返回可直接展示的清单。
 */
export function blockReasons(report: Report | null | undefined): string[] {
  if (!report) return ['尚未取得校验结论。']
  const out: string[] = []
  if (report.covered_nodes !== report.content_nodes) {
    const missing = report.content_nodes - report.covered_nodes
    if (missing > 0) out.push(`还有 ${missing} 段原文未被任何章节收录。`)
  }
  if (report.duplicated_nodes > 0) out.push(`有 ${report.duplicated_nodes} 段原文被重复收录。`)
  if (report.unknown_refs > 0) out.push(`有 ${report.unknown_refs} 个引用指向不存在的节点。`)
  for (const d of report.diagnostics ?? []) {
    if (d.severity === 'error') out.push(d.message)
  }
  return out.length > 0 ? [...new Set(out)] : []
}
