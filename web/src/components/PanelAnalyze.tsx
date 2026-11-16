/**
 * 分析区。
 *
 * 点「开始分析」→ POST /api/documents/{id}/plan 拿 job_id → EventSource 订阅
 * GET /api/jobs/{job_id}/events，实时显示中文进度；结束后展示 plan 与 report，
 * 并把 report 的诊断列成列表（error 红 / warning 黄 / info 灰）。
 */
import { useEffect, useMemo, useState } from 'react'
import { Button, ToggleButton, ToggleButtonGroup } from '@heroui/react'
import {
  SEVERITY_LABEL,
  editLabel,
  reportOK,
  type Diagnostic,
  type Severity,
} from '../api/types'
import { diagnosticsOf, issueCounts } from '../lib/report'
import {
  canRender,
  downloadBlockReason,
  gov,
  renderDocument,
  showTab,
  startAnalysis,
} from '../store/govdoc'
import { useGov } from '../store/useGov'
import {
  AlertBox,
  Badge,
  Btn,
  CheckboxField,
  EmptyState,
  Field,
  IndeterminateProgress,
  InlineSpinner,
  LinkButton,
  Row,
  SectionCard,
  TextInput,
} from '../ui/primitives'

/** 每个阶段的默认中文说明，用于 SSE 还没推消息时的占位。 */
const STAGE_LABEL: Record<string, string> = {
  connect: '正在连接模型服务…',
  analyze: '正在分析文档结构…',
  repair: '正在修复模型遗漏的节点…',
  validate: '正在校验方案完整性…',
}

/** 把服务端推来的阶段按固定顺序排列，未出现的阶段以「等待中」显示。 */
const STAGE_ORDER = ['connect', 'analyze', 'repair', 'validate']

interface StageRow {
  name: string
  label: string
  message: string
  done: boolean
  active: boolean
}

/** 严重级别的显示名。 */
function sevLabel(s: Severity): string {
  return SEVERITY_LABEL[s]
}

function diagClass(sev: Severity): string {
  if (sev === 'error') return 'border-s-danger bg-danger-soft text-danger-soft-foreground'
  if (sev === 'warning') return 'border-s-warning bg-warning-soft text-warning-soft-foreground'
  return 'border-s-border bg-default-soft text-default-soft-foreground'
}

export default function PanelAnalyze({ onReview }: { onReview: () => void }) {
  useGov()

  const [hint, setHint] = useState('')
  /** 只显示某一级别的诊断；null 表示全部。 */
  const [filter, setFilter] = useState<Severity | null>(null)

  /**
   * 是否允许模型修正错别字/语病/用词。
   *
   * 初值取服务端保存的偏好（配置区里设过就沿用），用户可以在本次分析上临时改。
   */
  const [allowEdits, setAllowEdits] = useState(false)

  const upload = gov.upload
  const plan = gov.plan
  const report = gov.report
  const render = gov.render
  const analyzing = gov.analyzing
  const analysisError = gov.analysisError
  const rendering = gov.rendering
  const stages = gov.stages

  const docId = upload?.doc_id ?? ''
  const llmReady = gov.health?.llm_configured ?? false

  const stageRows: StageRow[] = (() => {
    const seen = new Map(stages.map((s) => [s.name, s]))
    const names = STAGE_ORDER.filter((n) => seen.has(n) || analyzing)
    for (const s of stages) if (!names.includes(s.name)) names.push(s.name)

    const last = stages[stages.length - 1]
    return names.map((name) => {
      const s = seen.get(name)
      return {
        name,
        label: STAGE_LABEL[name] ?? name,
        message: s?.message ?? (analyzing ? '等待中…' : '未执行'),
        done: s?.done ?? false,
        active: analyzing && last?.name === name && !s?.done,
      }
    })
  })()

  const counts = useMemo(() => issueCounts(report), [report])

  const visibleDiagnostics = useMemo<Diagnostic[]>(() => {
    if (filter === null) return report?.diagnostics ?? []
    return diagnosticsOf(report, filter)
  }, [report, filter])

  const ok = useMemo(() => reportOK(report), [report])

  const coveragePct = useMemo(() => {
    if (!report || report.content_nodes === 0) return 0
    return Math.round((report.covered_nodes / report.content_nodes) * 100)
  }, [report])

  useEffect(() => {
    setHint('')
    setFilter(null)
    // 换文档时回到服务端保存的偏好，而不是沿用上一份文档的临时选择。
    setAllowEdits(gov.config?.allow_edits ?? false)
  }, [docId])

  const configAllowEdits = gov.config?.allow_edits
  useEffect(() => {
    if (configAllowEdits !== undefined) setAllowEdits(configAllowEdits)
  }, [configAllowEdits])

  async function analyze(): Promise<void> {
    if (docId === '') return
    if (!llmReady) {
      // 仍然允许尝试，但先说清后果：服务端会返回 llm_not_configured。
      setHint((h) => h) // 保留用户输入
    }
    await startAnalysis(docId, hint, undefined, allowEdits)
  }

  /**
   * 不调用模型，直接按原文自带的层次序数生成结构。
   *
   * 这不是"降级版分析"：原文已经编了号时，章节归属是读出来的而不是判断出来的，
   * 这份方案本来就是原文的忠实反映。它的价值在没有模型可用时最明显——至少能
   * 出一份结构合理、可以手工调整的成品，而不是什么都做不了。
   */
  async function analyzeBaseline(): Promise<void> {
    if (docId === '') return
    await startAnalysis(docId, '', undefined, undefined, true)
  }

  // ---------------------------------------------------------------- 傻瓜式主行动

  /**
   * 校验通过 → 直接把「下载公文文档」摆到最显眼的位置。
   *
   * 用户的要求是「不用再人工校对」：方案一旦通过校验，生成并下载就是唯一该做的
   * 事，校对区降级为可选的高级功能。因此这里的主按钮不是「进入校对区」，而是
   * 直接成文；成文成功后 renderDocument 会自动触发下载，「下载公文文档」按钮
   * 随即在顶部操作条与结果卡片出现，用户不必再找。
   */
  const readyToRender = reportOK(report) && plan !== null
  const downloadReady = render !== null && reportOK(report)
  const downloadHint = downloadBlockReason.value

  async function generate(): Promise<void> {
    await renderDocument(docId)
  }

  /** 本次方案声明的改动条数与类型摘要，让用户知道模型到底动了什么。 */
  const editSummary = useMemo(() => {
    const edits = plan?.edits ?? []
    const labels = new Map<string, number>()
    for (const e of edits) labels.set(e.kind, (labels.get(e.kind) ?? 0) + 1)
    return {
      total: edits.length,
      parts: [...labels.entries()].map(([k, n]) => `${editLabel(k)} ${n}`),
    }
  }, [plan])

  const statusBadge = analyzing ? (
    <Badge color="accent" className="gap-1">
      <span
        aria-hidden="true"
        className="size-[7px] shrink-0 animate-pulse rounded-full bg-current"
      />
      进行中
    </Badge>
  ) : plan ? (
    <Badge color={ok ? 'success' : 'danger'}>
      {ok ? '校验通过' : '校验未通过'}
    </Badge>
  ) : null

  return (
    <section className="flex min-w-0 flex-col gap-5">
      <header className="flex flex-wrap items-baseline gap-2.5">
        <h2 className="text-xl font-semibold leading-tight">分析文档结构</h2>
      </header>

      {!upload ? (
        <SectionCard>
          <EmptyState>
            <div>还没有上传文档。</div>
          </EmptyState>
        </SectionCard>
      ) : (
        <>
          {!llmReady && (
            <AlertBox status="warning" title="尚未配置模型">
              <div>
                {/* 就地给出入口：让人先回顶部找那个小按钮，是这一步最容易卡住的地方。 */}
                <Button variant="secondary" size="sm" onPress={() => showTab('settings')}>
                  去设置模型
                </Button>
              </div>
            </AlertBox>
          )}

          {/*
            不调用模型的入口。独立成块，而不是塞进上面那条警告里：它并不是
            "没配模型时的将就"，而是一条正当的路径 —— 原文自己编了号时，章节
            归属是读出来的，本来就无须模型判断。
          */}
          <SectionCard compact>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="text-sm font-medium">按原文编号生成结构</div>
              <Button
                variant="secondary"
                size="sm"
                isDisabled={analyzing || docId === ''}
                onPress={() => void analyzeBaseline()}
              >
                直接生成
              </Button>
            </div>
          </SectionCard>

          {/* 启动 + 进度 */}
          <SectionCard
            title="分析进度"
            actions={statusBadge}
            footer={
              <div className="flex w-full justify-end">
                <Button
                  variant="primary"
                  isDisabled={analyzing || docId === ''}
                  onPress={() => void analyze()}
                >
                  {analyzing && (
                    <span aria-hidden="true" className="inline-flex">
                      <InlineSpinner />
                    </span>
                  )}
                  {analyzing ? '分析中…' : plan ? '重新分析' : '开始分析'}
                </Button>
              </div>
            }
          >
            <Field
              label="补充说明"
              htmlFor="plan-hint"
            >
              <TextInput
                id="plan-hint"
                type="text"
                placeholder="例如：这是一份通知，请按通报格式梳理"
                value={hint}
                disabled={analyzing}
                autoComplete="off"
                onChange={(e) => setHint(e.target.value)}
              />
            </Field>

            <CheckboxField
              isSelected={allowEdits}
              isDisabled={analyzing}
              onChange={setAllowEdits}
              label="允许模型修正错别字、语病与用词"
            />

            {stageRows.length > 0 && (
              <div className="flex flex-col gap-0.5">
                {stageRows.map((s) => (
                  <div
                    key={s.name}
                    className={[
                      'flex items-start gap-2.5 rounded-lg px-2.5 py-2 text-sm transition-colors',
                      s.active
                        ? 'bg-accent-soft text-accent-soft-foreground'
                        : s.done
                          ? 'text-foreground'
                          : 'text-muted',
                    ].join(' ')}
                  >
                    <span
                      aria-hidden="true"
                      className={[
                        'mt-0.5 grid size-[18px] shrink-0 place-items-center rounded-full text-[11px] font-bold leading-none',
                        s.active
                          ? 'bg-accent text-accent-foreground'
                          : s.done
                            ? 'bg-success text-success-foreground'
                            : 'bg-default',
                      ].join(' ')}
                    >
                      {s.done ? (
                        '✓'
                      ) : s.active ? (
                        <InlineSpinner className="size-3" />
                      ) : (
                        '·'
                      )}
                    </span>
                    <span className="min-w-0">{s.message}</span>
                  </div>
                ))}
              </div>
            )}

            {analyzing && <IndeterminateProgress label="分析进行中" />}

            {analysisError && (
              <AlertBox
                status={plan ? 'warning' : 'danger'}
                title={plan ? '方案已生成，但未通过自动校验' : '分析未完成'}
              >
                {/* 服务端在自修复失败时会连**部分方案**一起返回；此时方案是可用的，
                    只是需要人工修复，措辞必须区别于「什么都没拿到」。 */}
                <span className="block">{analysisError}</span>
              </AlertBox>
            )}
          </SectionCard>

          {/*
            傻瓜式主行动。
            校验一旦通过，这里就是整页最显眼的东西：用户不需要先「进入校对区」，
            直接一步生成并下载。校对区因此降级为可选的高级功能（下方显式的次级入口）。
          */}
          {readyToRender && (
            <SectionCard
              className={[
                'gap-3.5 p-5',
                downloadReady
                  ? 'ring-1 ring-inset ring-success'
                  : 'ring-1 ring-inset ring-accent',
              ].join(' ')}
            >
              <div className="flex min-w-0 items-start gap-3">
                <div
                  aria-hidden="true"
                  className={[
                    'grid size-9 shrink-0 place-items-center rounded-full text-lg font-bold leading-none',
                    downloadReady
                      ? 'bg-success text-success-foreground'
                      : 'bg-accent text-accent-foreground',
                  ].join(' ')}
                >
                  {downloadReady ? '✓' : '↓'}
                </div>
                <div className="flex min-w-0 flex-col gap-1">
                  <div className="text-base font-semibold leading-snug">
                    {downloadReady
                      ? '公文文档已生成，可以直接下载'
                      : '校验通过，可以生成公文文档了'}
                  </div>
                  {editSummary.total > 0 && (
                    <Row>
                      <Badge color="warning">
                        已声明改动 {editSummary.total} 处
                      </Badge>
                      {editSummary.parts.map((p) => (
                        <Badge key={p}>{p}</Badge>
                      ))}
                    </Row>
                  )}
                </div>
              </div>

              <div className="flex flex-wrap items-center gap-2">
                {/* 已有文件时，「下载公文文档」就是主按钮；否则先「生成公文文档」。 */}
                {downloadReady ? (
                  <LinkButton
                    size="lg"
                    href={`api/files/${render?.file_id}`}
                    download
                  >
                    下载公文文档
                  </LinkButton>
                ) : (
                  <Btn
                    size="lg"
                    variant="primary"
                    isDisabled={!canRender.value}
                    title={canRender.value ? '' : '方案有未提交的修改或校验未通过'}
                    onPress={() => void generate()}
                  >
                    {rendering && (
                      <span aria-hidden="true" className="inline-flex">
                        <InlineSpinner />
                      </span>
                    )}
                    {rendering ? '生成中…' : '生成公文文档'}
                  </Btn>
                )}

                {downloadReady && (
                  <Button
                    variant="secondary"
                    isDisabled={rendering}
                    onPress={() => void generate()}
                  >
                    重新生成
                  </Button>
                )}
              </div>

              {downloadHint !== '' && (
                <div className="text-xs text-muted">{downloadHint}</div>
              )}
            </SectionCard>
          )}

          {/* 校验未通过：说清下一步仍是「生成」，只是需要先修掉问题 */}
          {!readyToRender && plan && report && (
            <div className="flex flex-col gap-2 rounded-3xl bg-surface-secondary p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-sm font-medium">校验未通过</span>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    variant="secondary"
                    size="sm"
                    onPress={onReview}
                  >
                    打开高级校对
                  </Button>
                </div>
              </div>
            </div>
          )}

          {/* 校验结论 */}
          {report && (
            <SectionCard
              title="校验结论"
              footer={
                <div className="flex w-full flex-wrap items-center justify-end gap-2">
                  <Button variant="ghost" onPress={onReview}>
                    打开高级校对（可选）
                  </Button>
                </div>
              }
            >
              <div className="grid grid-cols-[repeat(auto-fit,minmax(120px,1fr))] gap-3">
                <div className="flex flex-col gap-0.5">
                  <div
                    className={[
                      'text-2xl font-semibold leading-tight tabular-nums',
                      ok ? 'text-success-soft-foreground' : 'text-danger',
                    ].join(' ')}
                  >
                    {report.covered_nodes}/{report.content_nodes}
                  </div>
                  <div className="text-xs text-muted">正文节点已覆盖</div>
                </div>
                <div className="flex flex-col gap-0.5">
                  <div
                    className={[
                      'text-2xl font-semibold leading-tight tabular-nums',
                      report.duplicated_nodes > 0 ? 'text-danger' : '',
                    ].join(' ')}
                  >
                    {report.duplicated_nodes}
                  </div>
                  <div className="text-xs text-muted">重复收录</div>
                </div>
                <div className="flex flex-col gap-0.5">
                  <div
                    className={[
                      'text-2xl font-semibold leading-tight tabular-nums',
                      report.unknown_refs > 0 ? 'text-danger' : '',
                    ].join(' ')}
                  >
                    {report.unknown_refs}
                  </div>
                  <div className="text-xs text-muted">无效引用</div>
                </div>
                <div className="flex flex-col gap-0.5">
                  <div className="text-2xl font-semibold leading-tight tabular-nums">
                    {coveragePct}%
                  </div>
                  <div className="text-xs text-muted">覆盖率</div>
                </div>
              </div>

              <Row>
                <Badge color="danger">错误 {counts.error}</Badge>
                <Badge color="warning">警告 {counts.warning}</Badge>
                <Badge>提示 {counts.info}</Badge>
                {report.synthesized_headings > 0 && (
                  <Badge>模型自拟标题 {report.synthesized_headings}</Badge>
                )}
                {report.reordered_sections > 0 && (
                  <Badge>节内顺序被调整 {report.reordered_sections} 节</Badge>
                )}
                {(report.sections_out_of_order ?? 0) > 0 && (
                  <Badge color="warning">
                    章节顺序与原文不同 {report.sections_out_of_order} 处
                  </Badge>
                )}
                {report.bound_elements > 0 && (
                  <Badge>已绑定公文要素 {report.bound_elements}</Badge>
                )}
              </Row>

              {/* 级别筛选是单选组：用 ToggleButtonGroup 才有 aria-selected 与
                  方向键切换，手写一排 aria-pressed 的按钮只是看起来像。 */}
              <ToggleButtonGroup
                aria-label="按严重级别筛选诊断"
                selectionMode="single"
                selectedKeys={[filter ?? 'all']}
                onSelectionChange={(keys) => {
                  const next = [...keys][0]
                  if (next == null) return
                  setFilter(next === 'all' ? null : (String(next) as Severity))
                }}
                className="self-start"
              >
                <ToggleButton id="all" size="sm" className="px-3">
                  全部 {(report.diagnostics ?? []).length}
                </ToggleButton>
                {(['error', 'warning', 'info'] as Severity[]).map((s) => (
                  <ToggleButton key={s} id={s} size="sm" className="px-3">
                    {sevLabel(s)} {counts[s]}
                  </ToggleButton>
                ))}
              </ToggleButtonGroup>

              {visibleDiagnostics.length > 0 ? (
                <div className="flex max-h-[22rem] flex-col gap-1.5 overflow-y-auto pr-0.5">
                  {visibleDiagnostics.map((d, i) => (
                    <div
                      key={`${d.code}-${i}`}
                      className={[
                        'flex items-start gap-2.5 rounded-lg border-s-[3px] px-3 py-2.5 text-[13px] leading-relaxed',
                        diagClass(d.severity),
                      ].join(' ')}
                    >
                      <span className="shrink-0 font-mono text-[11px] opacity-75">
                        {d.code}
                      </span>
                      <span className="min-w-0 break-words">{d.message}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <EmptyState>
                  <div>{filter === null ? '没有任何诊断。' : '该级别下没有问题。'}</div>
                </EmptyState>
              )}
            </SectionCard>
          )}
        </>
      )}
    </section>
  )
}
