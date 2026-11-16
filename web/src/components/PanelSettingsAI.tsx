/**
 * AI（模型）设置。
 *
 * 从上传区搬到这里：模型连接是**应用级**设置，与某一份文档无关，
 * 放在上传区会让人以为换个文件就要重填一次。API Key 只写不回显，界面只显示
 * 「已配置」，这也是为什么清空输入框不等于清除密钥。
 *
 * 行文方向不在这里：它描述的是「这份文件发给谁」，不是「模型怎么连」，
 * 放在同一张卡片里会让用户在改 API Key 时顺手改掉版式。它现在属于「其他设置」。
 */
import { useState } from 'react'
import { Button } from '@heroui/react'
import { api, describeError } from '../api/client'
import { gov, notify, saveConfig } from '../store/govdoc'
import { useGov } from '../store/useGov'
import {
  Field,
  InlineSpinner,
  SectionCard,
  Select,
  TextInput,
} from '../ui/primitives'

interface FormState {
  endpoint: string
  model: string
  protocol: string
  apiKey: string
}

/**
 * 协议：空字符串表示让服务端按接口地址推断。
 *
 * 选择器不接受空键（空串在那里表示"还没选"），所以「自动识别」用一个哨兵值
 * 染色，只在表单与选择器之间转换，落进 FormState 之前就还原成空串。
 */
const AUTO_PROTOCOL = '__auto__'

const PROTOCOLS = [
  { value: AUTO_PROTOCOL, label: '自动识别' },
  { value: 'openai-chat', label: 'OpenAI Chat Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic', label: 'Anthropic Messages' },
]

export default function PanelSettingsAI() {
  useGov()

  const [form, setForm] = useState<FormState>(() => ({
    endpoint: gov.config?.llm.endpoint ?? '',
    model: gov.config?.llm.model ?? '',
    protocol: gov.config?.llm.protocol ?? '',
    apiKey: '',
  }))
  /** 获取成功的模型列表；空数组表示还没获取过。 */
  const [models, setModels] = useState<string[]>([])
  const [fetching, setFetching] = useState(false)
  const [fetchError, setFetchError] = useState('')

  function update<K extends keyof FormState>(key: K, value: FormState[K]): void {
    setForm((prev) => ({ ...prev, [key]: value }))
  }

  const keyState = gov.config?.llm.api_key_set ?? false

  /** 地址或协议改了之后，旧列表就属于另一个接口，不能继续拿来选。 */
  function invalidateModels(): void {
    if (models.length > 0) setModels([])
    if (fetchError !== '') setFetchError('')
  }

  async function fetchModels(): Promise<void> {
    const endpoint = form.endpoint.trim()
    if (endpoint === '') {
      setFetchError('请先填写接口地址。')
      return
    }
    setFetching(true)
    setFetchError('')
    try {
      const res = await api.listModels({
        endpoint,
        protocol: form.protocol,
        api_key: form.apiKey.trim(),
      })
      const list = res.models ?? []
      setModels(list)
      if (list.length === 0) {
        setFetchError('接口没有返回任何模型，请检查地址与协议。')
        return
      }
      // 现有模型不在新列表里时清空，避免保存一个下拉里选不到的旧值。
      if (form.model !== '' && !list.includes(form.model)) update('model', '')
      notify('ok', `已获取 ${list.length} 个模型，请在「模型名称」里选择。`)
    } catch (err) {
      setFetchError(describeError(err))
    } finally {
      setFetching(false)
    }
  }

  async function submit(): Promise<void> {
    if (form.endpoint.trim() === '') return
    if (form.model.trim() === '') return
    const ok = await saveConfig({
      endpoint: form.endpoint.trim(),
      model: form.model.trim(),
      protocol: form.protocol,
      apiKey: form.apiKey,
    })
    if (ok) update('apiKey', '')
  }

  const modelPlaceholder = fetching
    ? '正在获取模型…'
    : models.length > 0
      ? '请选择模型'
      : 'deepseek-chat'

  return (
    <SectionCard
      footer={
        <div className="flex justify-end">
          <Button
            type="button"
            variant="primary"
            isDisabled={
              gov.savingConfig || form.endpoint.trim() === '' || form.model.trim() === ''
            }
            onPress={() => {
              void submit()
            }}
          >
            {gov.savingConfig && <InlineSpinner />}
            {gov.savingConfig ? '保存中…' : '保存设置'}
          </Button>
        </div>
      }
    >
      {/* 接口地址与协议各占一半行宽。协议单独一行会把「连接到哪里」拆成
          两段视线，而这两项本来就是在描述同一个连接。 */}
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label="接口地址"
          htmlFor="set-endpoint"
        >
          <TextInput
            id="set-endpoint"
            type="url"
            inputMode="url"
            placeholder="https://api.deepseek.com/v1"
            autoComplete="off"
            value={form.endpoint}
            onChange={(e) => {
              update('endpoint', e.target.value)
              invalidateModels()
            }}
          />
        </Field>
        <Field label="接口协议" htmlFor="set-protocol">
          <Select
            id="set-protocol"
            aria-label="接口协议"
            value={form.protocol === '' ? AUTO_PROTOCOL : form.protocol}
            onChange={(value) => {
              update('protocol', value === AUTO_PROTOCOL ? '' : value)
              invalidateModels()
            }}
            items={PROTOCOLS.map((p) => ({ value: p.value, label: p.label }))}
          />
        </Field>
      </div>

      <Field label="API Key" htmlFor="set-key">
        <TextInput
          id="set-key"
          type="password"
          placeholder={keyState ? '已配置' : 'API Key'}
          autoComplete="new-password"
          value={form.apiKey}
          onChange={(e) => update('apiKey', e.target.value)}
        />
      </Field>

      <Field label="模型名称" htmlFor="set-model" error={fetchError}>
        <div className="grid grid-cols-[3fr_1fr] gap-2">
          <div className="min-w-0">
            {models.length > 0 ? (
              <Select
                id="set-model"
                aria-label="模型名称"
                value={form.model}
                onChange={(value) => update('model', value)}
                placeholder={modelPlaceholder}
                items={models.map((m) => ({ value: m, label: m }))}
              />
            ) : (
              <TextInput
                id="set-model"
                type="text"
                placeholder={modelPlaceholder}
                autoComplete="off"
                value={form.model}
                onChange={(e) => update('model', e.target.value)}
              />
            )}
          </div>
          <Button
            type="button"
            variant="secondary"
            className="w-full min-w-0 px-2"
            isDisabled={fetching || form.endpoint.trim() === ''}
            onPress={() => {
              void fetchModels()
            }}
          >
            {fetching && <InlineSpinner />}
            {fetching ? '获取中…' : '获取模型'}
          </Button>
        </div>
      </Field>

    </SectionCard>
  )
}
