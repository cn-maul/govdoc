/**
 * 分页预览页（全屏）。
 *
 * 纸张本身由 `PreviewPane` 渲染，与校对工作台左栏共用同一个组件 —— 两处看到的
 * 必须是同一张纸，否则用户在校对时对版面的判断会被"另一套渲染"推翻。
 *
 * 这一页只负责三件事：说明这是干什么的、给出规格徽标、把纸张铺满。
 */
import { editLabel } from '../api/types'
import { currentProfileName, gov } from '../store/govdoc'
import { useGov } from '../store/useGov'
import { Badge, EmptyState, Row, SectionCard } from '../ui/primitives'
import PreviewPane from './PreviewPane'

export default function PanelPreview() {
  useGov()

  const docId = gov.upload?.doc_id ?? ''

  /** 按类型汇总声明的改动，用于图例旁的小结。 */
  const declaredSummary = (() => {
    const map = new Map<string, number>()
    for (const e of gov.plan?.edits ?? []) map.set(e.kind, (map.get(e.kind) ?? 0) + 1)
    return [...map.entries()].map(([k, n]) => `${editLabel(k)} ${n}`)
  })()

  const specLine = (() => {
    const s = gov.specInfo?.spec
    if (!s) return ''
    return `A4 ${s.page.width_mm}×${s.page.height_mm}mm · 版心 156×225mm · 每面 ${s.page.lines_per_page} 行 × 每行 ${s.page.chars_per_line} 字`
  })()

  const spacingLine = (() => {
    const p = gov.specInfo?.spec?.page
    if (!p) return ''
    const linePT =
      ((p.height_mm - p.margin_top_mm - p.margin_bottom_mm) * (72 / 25.4)) /
      p.lines_per_page
    const pitch =
      ((p.width_mm - p.margin_left_mm - p.margin_right_mm) * (72 / 25.4)) /
      p.chars_per_line
    return `固定行距 ${linePT.toFixed(2)}pt · 每字 ${pitch.toFixed(2)}pt`
  })()

  return (
    <section className="flex min-w-0 flex-col gap-5">
      <header className="flex flex-wrap items-baseline gap-2.5">
        <h2 className="text-xl font-semibold leading-tight">分页预览</h2>
      </header>

      {docId === '' ? (
        <SectionCard>
          <EmptyState>
            <div>还没有可预览的文档。</div>
          </EmptyState>
        </SectionCard>
      ) : (
        <>
          <SectionCard compact>
            <Row>
              {currentProfileName.value && (
                <Badge color="accent">规则：{currentProfileName.value}</Badge>
              )}
              {specLine && <Badge color="accent">{specLine}</Badge>}
              {spacingLine && <Badge>{spacingLine}</Badge>}
              {declaredSummary.map((s) => (
                <Badge key={s}>{s}</Badge>
              ))}
            </Row>
          </SectionCard>

          <PreviewPane />
        </>
      )}
    </section>
  )
}
