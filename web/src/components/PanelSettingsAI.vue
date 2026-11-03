<script setup lang="ts">
/**
 * AI（模型）设置。
 *
 * 从上传区搬到这里：模型连接是**应用级**设置，与某一份文档无关，
 * 放在上传区会让人以为换个文件就要重填一次。API Key 只写不回显，界面只显示
 * 「已配置」，这也是为什么清空输入框不等于清除密钥。
 */
import { computed, reactive } from 'vue'
import type { Direction } from '../api/types'
import { gov, saveConfig } from '../composables/useGovdoc'

const form = reactive({
  endpoint: gov.config?.llm.endpoint ?? '',
  model: gov.config?.llm.model ?? '',
  protocol: gov.config?.llm.protocol ?? '',
  apiKey: '',
  direction: (gov.config?.direction ?? '下行文') as Direction,
  allowEdits: gov.config?.allow_edits ?? false,
})

/** 协议：空字符串表示让服务端按接口地址推断。 */
const PROTOCOLS = [
  { value: '', label: '自动识别' },
  { value: 'openai-chat', label: 'OpenAI Chat Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic', label: 'Anthropic Messages' },
]

const DIRECTIONS: { value: Direction; hint: string }[] = [
  { value: '下行文', hint: '发给下级机关' },
  { value: '上行文', hint: '报给上级机关' },
  { value: '平行文', hint: '发往平级机关' },
]

const keyState = computed(() => gov.config?.llm.api_key_set ?? false)
const llmReady = computed(() => gov.health?.llm_configured ?? false)

/**
 * 持久化状态。三种情况互斥，措辞必须各说各的。
 *
 * `saved_to_disk` 在**读取**时表示「磁盘上是否已有配置文件」，所以首次运行它
 * 是 false —— 那不是保存失败，该说的是「尚未保存过」。把它读成失败，会让用户
 * 第一次打开程序就被告知保存失败，而其实他什么都没保存。
 */
const persistent = computed(() => gov.config?.persistent ?? false)
const settingsPath = computed(() => gov.config?.settings_path ?? '')
const savedBefore = computed(() => gov.config?.saved_to_disk ?? false)

const persistState = computed(() => {
  if (gov.config?.save_warning) {
    return { tone: 'gd-alert--warning', title: '保存到磁盘失败', hint: gov.config.save_warning }
  }
  if (!persistent.value) {
    return {
      tone: 'gd-alert--warning',
      title: '配置仅在本次运行内有效',
      hint: '当前安装未启用配置持久化：保存只在本次运行内有效，重启后需要重新填写。',
    }
  }
  if (savedBefore.value) {
    return {
      tone: 'gd-alert--info',
      title: '配置已保存到程序目录',
      hint: settingsPath.value
        ? `配置写在 ${settingsPath.value}（程序所在目录），重启后仍然有效。`
        : '配置已写入磁盘，重启后仍然有效。',
    }
  }
  return {
    tone: 'gd-alert--info',
    title: '配置会保存到程序目录',
    hint: settingsPath.value
      ? `点「保存设置」后会写入 ${settingsPath.value}（程序所在目录），重启后仍然有效。`
      : '点「保存设置」后会写入程序所在目录，重启后仍然有效。',
  }
})

const protocolHint = computed(() => {
  const p = form.protocol
  if (p === '') return '留空时由服务端按接口地址推断协议。'
  if (p === 'anthropic') return 'Anthropic 协议，密钥以 x-api-key 发送。'
  return 'OpenAI 兼容协议，接口地址通常以 /v1 结尾。'
})

async function submit(): Promise<void> {
  if (form.endpoint.trim() === '') return
  if (form.model.trim() === '') return
  const ok = await saveConfig({
    endpoint: form.endpoint.trim(),
    model: form.model.trim(),
    protocol: form.protocol,
    apiKey: form.apiKey,
    direction: form.direction,
    allowEdits: form.allowEdits,
  })
  if (ok) form.apiKey = ''
}
</script>

<template>
  <div class="gd-card">
    <div class="gd-card__header">
      <div class="gd-card__headings">
        <div class="gd-card__title">AI 设置</div>
        <div class="gd-card__description">
          用于理解文档结构、梳理章节。API Key 只写不回显，界面只显示是否已配置。
        </div>
      </div>
      <span class="gd-badge" :class="llmReady ? 'gd-badge--success' : 'gd-badge--warning'">
        <span class="gd-dot" aria-hidden="true"></span>
        {{ llmReady ? '已配置' : '未配置' }}
      </span>
    </div>

    <div class="gd-field">
      <label class="gd-label" for="set-endpoint">接口地址</label>
      <input
        id="set-endpoint"
        v-model="form.endpoint"
        class="gd-input"
        type="url"
        inputmode="url"
        placeholder="https://api.deepseek.com/v1"
        autocomplete="off"
      />
      <div class="gd-field__hint">模型服务的根地址，通常以 /v1 结尾。</div>
    </div>

    <div class="gd-field-row">
      <div class="gd-field">
        <label class="gd-label" for="set-model">模型名称</label>
        <input
          id="set-model"
          v-model="form.model"
          class="gd-input"
          type="text"
          placeholder="deepseek-chat"
          autocomplete="off"
        />
      </div>
      <div class="gd-field">
        <label class="gd-label" for="set-protocol">接口协议</label>
        <select id="set-protocol" v-model="form.protocol" class="gd-select">
          <option v-for="p in PROTOCOLS" :key="p.value" :value="p.value">{{ p.label }}</option>
        </select>
      </div>
    </div>
    <div class="gd-field__hint">{{ protocolHint }}</div>

    <div class="gd-field">
      <label class="gd-label" for="set-key">
        API Key
        <span class="gd-label__optional">{{ keyState ? '已配置，留空则不修改' : '尚未配置' }}</span>
      </label>
      <input
        id="set-key"
        v-model="form.apiKey"
        class="gd-input"
        type="password"
        :placeholder="keyState ? '••••••••（留空表示保持不变）' : '粘贴你的 API Key'"
        autocomplete="new-password"
      />
      <div class="gd-field__hint">出于安全考虑，已保存的密钥不会回传到页面。</div>
    </div>

    <div class="gd-field">
      <span class="gd-label">行文方向</span>
      <div class="gd-segment" role="group" aria-label="行文方向">
        <button
          v-for="d in DIRECTIONS"
          :key="d.value"
          type="button"
          class="gd-segment__item"
          :aria-pressed="form.direction === d.value"
          :title="d.hint"
          @click="form.direction = d.value"
        >
          {{ d.value }}
        </button>
      </div>
      <div class="gd-field__hint">
        行文方向决定发文机关标志距版心上边缘的距离：上行文为 80mm，下行文与平行文为
        35mm（GB/T 9704-2012 §4.3.2）。仅对「公文」这类规则有意义。
      </div>
    </div>

    <div class="gd-field">
      <label class="gd-check">
        <input v-model="form.allowEdits" type="checkbox" />
        <span>
          <span class="gd-label">允许模型修正错别字、语病与用词</span>
          <span class="gd-field__hint">
            默认关闭，只梳理结构、不动一个字。开启后模型可以提出修正，但每一处修正都必须
            写明「原文什么 → 改成什么 → 为什么」，在校对区逐条可见、可拒绝；
            <strong>任何未声明的改写仍然会让导出失败</strong>。
          </span>
        </span>
      </label>
    </div>

    <div class="gd-alert" :class="persistState.tone">
      <span class="gd-alert__icon" aria-hidden="true">i</span>
      <div class="gd-alert__body">
        <div class="gd-alert__title">{{ persistState.title }}</div>
        <div>{{ persistState.hint }}</div>
      </div>
    </div>

    <div class="gd-card__footer">
      <button
        type="button"
        class="gd-btn gd-btn--primary"
        :disabled="gov.savingConfig || form.endpoint.trim() === '' || form.model.trim() === ''"
        @click="submit"
      >
        <span v-if="gov.savingConfig" class="gd-spinner" aria-hidden="true"></span>
        {{ gov.savingConfig ? '保存中…' : '保存设置' }}
      </button>
    </div>
  </div>
</template>