/**
 * 工作台顶栏：把「当前文件 + 规则 + 校验状态 + 三个动作」放进应用导航栏。
 *
 * 这些信息原先单独占工作台的一整行，在高度紧张的预览/校对界面上白白吃掉
 * 了六七十像素。放进导航栏后，工作台内部只剩左右两栏，第一屏就能看到更多
 * 正文与校对内容。
 */
import { Button, Chip } from '@heroui/react'
import { reportOK } from '../api/types'
import {
  currentProfileName,
  downloadRenderedFile,
  gov,
  reanalyze,
  renderDocument,
  resetDocument,
} from '../store/govdoc'
import { useGov } from '../store/useGov'
import { Btn, InlineSpinner, Row } from '../ui/primitives'

export default function WorkbenchBar() {
  useGov()

  if (!gov.upload) return null

  const docId = gov.upload?.doc_id ?? ''
  const ok = reportOK(gov.report)
  const ready = gov.render !== null && ok

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
        {gov.analyzing ? '分析中' : gov.report ? (ok ? '校验通过' : '有未处理问题') : '尚未分析'}
      </Chip>

      <span className="flex-1" />

      <Btn size="sm" variant="ghost" onPress={resetDocument}>
        换文档
      </Btn>
      <Btn size="sm" variant="secondary" isDisabled={gov.analyzing} onPress={() => void reanalyze()}>
        {gov.analyzing && <InlineSpinner className="size-3" />}
        {gov.plan ? '重新分析' : gov.analyzing ? '分析中…' : '开始分析'}
      </Btn>
      <Btn
        size="sm"
        variant="ghost"
        isDisabled={!gov.plan || gov.analyzing || gov.rendering}
        onPress={() => void renderDocument(docId)}
      >
        {gov.rendering ? '生成中…' : '生成公文'}
      </Btn>
      {gov.render && (
        <Button size="sm" variant="primary" isDisabled={!ready} onPress={downloadRenderedFile}>
          下载
        </Button>
      )}
    </Row>
  )
}
