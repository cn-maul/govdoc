/**
 * 工作台顶栏：把「当前文件 + 规则 + 校验状态 + 动作」放进应用导航栏。
 *
 * 这些信息原先单独占工作台的一整行，在高度紧张的预览/校对界面上白白吃掉
 * 了六七十像素。放进导航栏后，工作台内部只剩左右两栏，第一屏就能看到更多
 * 正文与校对内容。
 *
 * 动作的主次是按真实用法排的：上传后已经有一份本地规则排好的稿子，
 * 主按钮是「生成并下载」；AI 是可选的一步，放在次要位置。
 */
import { Button, Chip } from '@heroui/react'
import { diffClean, reportOK } from '../api/types'
import {
  currentProfileName,
  downloadRenderedFile,
  gov,
  notify,
  renderDocument,
  resetDocument,
  startAnalysis,
} from '../store/govdoc'
import { useGov } from '../store/useGov'
import { Btn, InlineSpinner, Row } from '../ui/primitives'

export default function WorkbenchBar() {
  useGov()

  if (!gov.upload) return null

  const docId = gov.upload?.doc_id ?? ''
  const ok = reportOK(gov.report)
  const downloadable =
    gov.render !== null && ok && (!gov.render.diff || diffClean(gov.render.diff))
  const configured = gov.health?.llm_configured ?? false

  async function aiProcess(): Promise<void> {
    if (docId === '') return
    if (!configured) {
      notify('info', '还没有配置模型。请点右上角的模型状态按钮填写接口地址与 API Key。')
      return
    }
    await startAnalysis(docId, '', undefined, gov.config?.allow_edits ?? true)
    // AI 处理完如果方案直接通过校验，顺手把文件备好，用户下一步就能下载。
    if (gov.plan && reportOK(gov.report)) {
      gov.preparing = true
      try {
        await renderDocument(docId, { download: false })
      } finally {
        gov.preparing = false
      }
    }
  }

  return (
    <Row className="min-w-0 gap-2">
      <span
        className="max-w-[16rem] truncate text-sm font-medium"
        title={gov.upload?.source_name}
      >
        {gov.upload?.source_name}
      </span>
      {currentProfileName.value && (
        <Chip color="accent" size="sm" className="shrink-0">
          {currentProfileName.value}
        </Chip>
      )}
      <Chip
        color={gov.analyzing ? 'accent' : ok ? 'success' : gov.report ? 'danger' : 'default'}
        size="sm"
        className="shrink-0"
      >
        {gov.analyzing
          ? gov.analysisMode === 'ai'
            ? 'AI 处理中'
            : '正在排版'
          : gov.render
            ? '可下载'
            : gov.preparing
              ? '正在备稿'
              : gov.report
                ? ok
                  ? '可下载'
                  : '有未处理问题'
                : '待排版'}
      </Chip>

      <span className="flex-1" />

      <Btn size="sm" variant="ghost" onPress={resetDocument}>
        换文档
      </Btn>
      <Btn
        size="sm"
        variant="ghost"
        isDisabled={gov.analyzing || !gov.plan}
        onPress={() => void aiProcess()}
      >
        {gov.analysisMode === 'ai' && <InlineSpinner className="size-3" />}
        {gov.analysisMode === 'ai' ? 'AI 处理中…' : 'AI 处理'}
      </Btn>
      <Button
        size="sm"
        variant="primary"
        isDisabled={gov.analyzing || !gov.plan || gov.rendering}
        onPress={downloadable ? downloadRenderedFile : () => void renderDocument(docId)}
      >
        {gov.rendering || gov.preparing
          ? '生成中…'
          : downloadable
            ? '下载公文'
            : gov.render
              ? '重新生成'
              : '生成并下载'}
      </Button>
    </Row>
  )
}
