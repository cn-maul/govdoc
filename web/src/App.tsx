/**
 * 应用壳。
 *
 * 只有两种状态：设置页、单页工作台。工作台从空白时就存在，上传入口嵌在
 * 预览区里；文档上传后是同一套页面，不再跳到另一个「上传/分析/校对」流程。
 */
import { useEffect } from 'react'
import { Button, Toast, cn, toast } from '@heroui/react'
import { bootstrap, closeEvents, dismissNotice, gov, showTab } from './store/govdoc'
import { useGov } from './store/useGov'
import PanelSettings from './components/PanelSettings'
import Workbench from './components/Workbench'
import WorkbenchBar from './components/WorkbenchBar'
import { Btn, SectionCard } from './ui/primitives'

export default function App() {
  useGov()

  useEffect(() => {
    void bootstrap()
  }, [])

  useEffect(() => {
    const handler = () => closeEvents()
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [])

  const notice = gov.notice
  useEffect(() => {
    if (!notice) return
    const variant = notice.kind === 'ok' ? 'success' : notice.kind === 'error' ? 'danger' : 'accent'
    toast(notice.text, {
      variant,
      timeout: notice.kind === 'error' ? 8000 : 4000,
    })
    dismissNotice()
  }, [notice])

  const configured = gov.health?.llm_configured ?? false
  const model = gov.config?.llm.model?.trim() ?? ''
  const status = gov.booting
    ? { color: 'accent' as const, text: '正在连接服务…' }
    : gov.bootError
      ? { color: 'danger' as const, text: '服务未就绪' }
      : configured
        ? { color: 'success' as const, text: model || '模型已配置' }
        : { color: 'warning' as const, text: '模型未配置，点此配置' }

  const onSettings = gov.tab === 'settings'

  return (
    <div className="flex h-screen flex-col overflow-hidden bg-background text-foreground">
      <header className="sticky top-0 z-40 border-b border-border bg-surface/95 backdrop-blur">
        <div className="mx-auto flex w-full max-w-[1600px] flex-wrap items-center justify-between gap-3 px-4 py-2.5 md:px-5">
          <button
            type="button"
            onClick={() => showTab('upload')}
            className="flex min-w-0 items-center gap-2.5 rounded-xl px-1 py-0.5 text-left"
          >
            <span
              aria-hidden="true"
              className="grid size-9 shrink-0 place-items-center rounded-xl bg-accent text-base font-semibold text-accent-foreground"
            >
              公
            </span>
            <span className="truncate text-[15px] font-semibold">GovDoc 文档排版合规化</span>
          </button>
          <div className="flex flex-wrap items-center gap-2">
            {!onSettings && <WorkbenchBar />}
            <Btn
              size="sm"
              variant={onSettings ? 'secondary' : configured ? 'ghost' : 'outline'}
              aria-current={onSettings ? 'page' : undefined}
              onPress={() => showTab('settings')}
              className={cn('gap-1.5', !gov.booting && !configured && 'ring-1 ring-warning')}
            >
              <span
                aria-hidden="true"
                className={cn(
                  'size-[7px] shrink-0 rounded-full',
                  status.color === 'success' && 'bg-success',
                  status.color === 'warning' && 'bg-warning',
                  status.color === 'danger' && 'bg-danger',
                  status.color === 'accent' && 'animate-pulse bg-accent',
                )}
              />
              {status.text}
              <span aria-hidden="true" className="opacity-60">
                ⚙
              </span>
            </Btn>
          </div>
        </div>
      </header>

      <Toast.Provider placement="top" maxVisibleToasts={3} />

      {onSettings ? (
        <main className="mx-auto w-full max-w-5xl flex-1 overflow-y-auto px-4 py-5 md:px-5">
          <div className="mb-3">
            <Button variant="ghost" size="sm" onPress={() => showTab('upload')}>
              ← 返回
            </Button>
          </div>
          {gov.booting ? (
            <SectionCard>
              <div className="space-y-3">
                <div className="h-5 w-2/5 animate-pulse rounded bg-surface-tertiary" />
                <div className="h-5 w-3/4 animate-pulse rounded bg-surface-tertiary" />
                <div className="h-5 w-1/2 animate-pulse rounded bg-surface-tertiary" />
              </div>
            </SectionCard>
          ) : (
            <PanelSettings />
          )}
        </main>
      ) : gov.booting ? (
        <main className="mx-auto w-full max-w-5xl flex-1 px-4 py-6 md:px-6">
          <SectionCard>
            <div className="space-y-3">
              <div className="h-5 w-2/5 animate-pulse rounded bg-surface-tertiary" />
              <div className="h-5 w-3/4 animate-pulse rounded bg-surface-tertiary" />
              <div className="h-5 w-1/2 animate-pulse rounded bg-surface-tertiary" />
            </div>
          </SectionCard>
        </main>
      ) : (
        <Workbench />
      )}
    </div>
  )
}
