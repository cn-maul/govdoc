/**
 * 上传区。
 *
 * 只做两件事：选文件、选用哪套排版规则。模型连接已移到「设置」页 ——
 * 那是应用级设置，放在这里会让人以为每换一份文件就要重填一次 API Key。
 *
 * 排版规则在这里选，是因为它是**逐文档**的选择：同一批文件里可能既有公文也有
 * 报告，各用各的版式。规则一旦在上传时确定，文档就记住了它，之后改默认配置
 * 也不会把已上传的文档换掉版式。
 */
import { useMemo, useRef, useState } from 'react'
import type { ChangeEvent, DragEvent } from 'react'
import { Button } from '@heroui/react'
import { KIND_LABEL, specSummary, type NodeSummary } from '../api/types'
import {
  currentProfileName,
  gov,
  loadSource,
  notify,
  uploadDocument,
} from '../store/govdoc'
import { useGov } from '../store/useGov'
import {
  Badge,
  Field,
  InlineSpinner,
  Row,
  SectionCard,
  Select,
  Stack,
} from '../ui/primitives'

/** 允许的扩展名与后端 internal/parse 支持的一致。 */
const ACCEPT = '.docx,.txt,.md'

export default function PanelUpload({ onDone }: { onDone: () => void }) {
  useGov()

  // ---------------------------------------------------------------- 上传

  const inputRef = useRef<HTMLInputElement | null>(null)
  const [dragging, setDragging] = useState(false)

  const upload = gov.upload
  const uploading = gov.uploading
  const profiles = gov.profiles

  function pickFile(): void {
    inputRef.current?.click()
  }

  function onPicked(ev: ChangeEvent<HTMLInputElement>): void {
    const input = ev.target
    const file = input.files?.[0]
    if (file) void beginUpload(file)
    // 允许重复选择同一个文件。
    input.value = ''
  }

  function onDrop(ev: DragEvent<HTMLDivElement>): void {
    setDragging(false)
    const file = ev.dataTransfer?.files?.[0]
    if (!file) {
      notify('error', '没有检测到文件，请重新拖入或点击选择。')
      return
    }
    void beginUpload(file)
  }

  function onDragOver(ev: DragEvent<HTMLDivElement>): void {
    ev.preventDefault()
    setDragging(true)
  }

  function onDragLeave(): void {
    setDragging(false)
  }

  async function beginUpload(file: File): Promise<void> {
    const ok = await uploadDocument(file, gov.selectedProfile)
    if (ok) {
      // 顺带把原文读回来，校对区左栏才能立刻逐字显示。
      if (gov.upload) void loadSource(gov.upload.doc_id)
    }
  }

  // ---------------------------------------------------------------- 排版规则

  /** 选中的那套规则的摘要，随选择即时变化。 */
  const selectedSummary = useMemo(() => {
    const p = gov.profiles.find((x) => x.id === gov.selectedProfile)
    return p ? specSummary(p.spec) : ''
  }, [gov.profiles, gov.selectedProfile])

  const nodeStats = useMemo(() => {
    const nodes: NodeSummary[] = gov.upload?.nodes ?? []
    const byKind = new Map<string, number>()
    for (const n of nodes) byKind.set(n.kind, (byKind.get(n.kind) ?? 0) + 1)
    return [...byKind.entries()].map(([kind, count]) => ({
      kind,
      label: KIND_LABEL[kind as NodeSummary['kind']] ?? kind,
      count,
    }))
  }, [gov.upload])

  return (
    <section className="flex min-w-0 flex-col gap-5">
      <header className="flex flex-wrap items-baseline gap-2.5">
        <h2 className="text-xl font-semibold leading-tight">上传文档</h2>
      </header>

      <div className="grid grid-cols-[repeat(auto-fit,minmax(280px,1fr))] items-start gap-4">
        {/* 排版规则 */}
        <SectionCard
          title="排版规则"
          actions={
            profiles.length > 0 ? (
              <Badge color="accent">{profiles.length} 套可选</Badge>
            ) : null
          }
        >
          <Field
            label="使用哪套配置"
            htmlFor="pick-profile"
            hint={selectedSummary || undefined}
          >
            <Select
              id="pick-profile"
              aria-label="使用哪套配置"
              value={gov.selectedProfile}
              onChange={(id) => {
                gov.selectedProfile = id
              }}
              items={profiles.map((p) => ({
                value: p.id,
                label: p.name + (p.id === gov.activeProfile ? '（默认）' : ''),
              }))}
            />
          </Field>

          {upload && (
            <Badge color="accent">本文档使用「{currentProfileName.value}」</Badge>
          )}
        </SectionCard>

        {/* 上传 */}
        <SectionCard
          title="源文件"
          actions={upload ? <Badge color="success">{upload.format}</Badge> : null}
        >
          {/* 拖放监听挂在原生 div 上：HeroUI 的 Button 会把 onDrag* 过滤掉
              （react-aria 只放行它自己的按压/焦点事件），而拖放是原生 DOM 行为。
              点击仍走 HeroUI Button —— 键盘激活与焦点环由组件库负责，比手搓
              role="button" 可靠。文件选择器保持原生 input，那是唯一能弹系统对话框的方式。 */}
          <div
            data-active={dragging ? 'true' : 'false'}
            onDragOver={onDragOver}
            onDragLeave={onDragLeave}
            onDrop={onDrop}
            className="rounded-3xl data-[active=true]:bg-accent-soft data-[active=true]:ring-2 data-[active=true]:ring-inset data-[active=true]:ring-accent"
          >
            <Button
              variant="ghost"
              fullWidth
              onPress={pickFile}
              className="flex h-auto flex-col items-center justify-center gap-2 rounded-3xl bg-surface-secondary px-4 py-8 text-center transition-colors hover:bg-surface-tertiary"
            >
              <span className="text-sm font-medium">
                {uploading ? (
                  <>
                    <span aria-hidden="true" className="mr-1 inline-flex align-middle">
                      <InlineSpinner />
                    </span>{' '}
                    正在解析文件…
                  </>
                ) : upload ? (
                  `已上传「${upload.source_name}」`
                ) : (
                  '把文件拖到这里，或点击选择'
                )}
              </span>
            </Button>
          </div>
          <input
            ref={inputRef}
            className="sr-only"
            type="file"
            accept={ACCEPT}
            aria-label="选择源文件"
            onChange={onPicked}
          />

          {upload && (
            <Stack className="gap-2">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-[13px] text-muted">
                  共 {upload.node_count} 个块，其中正文 {upload.content_count} 段
                </span>
                <Button variant="ghost" size="sm" onPress={pickFile}>
                  更换文件
                </Button>
              </div>
              <Row>
                {nodeStats.map((s) => (
                  <Badge key={s.kind}>
                    {s.label} {s.count}
                  </Badge>
                ))}
              </Row>
            </Stack>
          )}
        </SectionCard>
      </div>

      <div className="flex justify-end">
        <Button
          variant="primary"
          size="lg"
          isDisabled={!upload}
          onPress={onDone}
        >
          下一步：分析文档结构
        </Button>
      </div>
    </section>
  )
}
