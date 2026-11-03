<script setup lang="ts">
/**
 * 应用外壳。
 *
 * 三区（上传与配置 / 分析 / 校对与输出）加预览，用左侧导航切换。
 * 导航项根据当前进度给出禁用与副标题提示，让用户知道「下一步能做什么」。
 */
import { computed, onMounted } from 'vue'
import {
  bootstrap,
  closeEvents,
  currentProfileName,
  dismissNotice,
  downloadRenderedFile,
  gov,
  hasDownload,
  type TabKey,
} from './composables/useGovdoc'
import PanelUpload from './components/PanelUpload.vue'
import PanelAnalyze from './components/PanelAnalyze.vue'
import PanelReview from './components/PanelReview.vue'
import PanelPreview from './components/PanelPreview.vue'
import PanelSettings from './components/PanelSettings.vue'
import { reportOK } from './api/types'

onMounted(() => {
  void bootstrap()
})

// 组件卸载时关闭可能仍打开的 SSE 连接，避免热更新后连接泄漏。
window.addEventListener('beforeunload', closeEvents)

interface NavItem {
  key: TabKey
  index: string
  label: string
  sub: string
  enabled: boolean
}

const navItems = computed<NavItem[]>(() => {
  const hasDoc = gov.upload !== null
  const hasPlan = gov.plan !== null
  const passed = reportOK(gov.report)
  return [
    {
      key: 'upload',
      index: '1',
      label: '上传文档',
      sub: gov.upload ? gov.upload.source_name : '选择文件与排版规则',
      enabled: true,
    },
    {
      key: 'analyze',
      index: '2',
      label: '分析与生成',
      sub: hasPlan
        ? passed
          ? gov.render
            ? '文件已就绪，可下载'
            : '校验通过，可生成并下载'
          : '需处理几个问题'
        : hasDoc
          ? '等待开始分析'
          : '需要先上传文档',
      enabled: hasDoc,
    },
    {
      key: 'review',
      index: '3',
      label: '高级校对（可选）',
      sub: hasPlan
        ? (gov.plan?.edits?.length ?? 0) > 0
          ? `审阅 ${gov.plan?.edits?.length} 处改动 / 修复漏收重复`
          : '修复漏收/重复，逐条审阅改动'
        : '需要先生成方案',
      enabled: hasPlan,
    },
    {
      key: 'preview',
      index: '4',
      label: '分页预览',
      sub: hasDoc ? '按所选规则模拟版面' : '需要先上传文档',
      enabled: hasDoc,
    },
    {
      key: 'settings',
      index: '⚙',
      label: '设置',
      sub: gov.config?.llm.api_key_set ? '模型与排版规则' : '尚未配置模型',
      // 设置永远可用：还没配置模型时，正是最需要进来看说明的地方。
      enabled: true,
    },
  ]
})

function go(key: TabKey): void {
  const item = navItems.value.find((n) => n.key === key)
  if (item && !item.enabled) return
  gov.tab = key
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

const statusBadge = computed(() => {
  if (gov.booting) return { cls: 'gd-badge--accent', text: '正在连接服务…' }
  if (gov.bootError) return { cls: 'gd-badge--danger', text: '服务未就绪' }
  const configured = gov.health?.llm_configured
  return configured
    ? { cls: 'gd-badge--success', text: `模型已配置${gov.health?.version ? ' · v' + gov.health.version : ''}` }
    : { cls: 'gd-badge--warning', text: '模型未配置' }
})

/** 服务端返回的校验/渲染错误也应当在顶部可见。 */
const noticeKind = computed(() => gov.notice?.kind ?? 'info')
</script>

<template>
  <div class="gd-app">
    <header class="gd-header">
      <div class="gd-shell gd-header__inner">
        <div class="gd-brand">
          <span class="gd-brand__mark" aria-hidden="true">公</span>
          <span class="gd-brand__text">
            <span class="gd-brand__name">GovDoc 文档排版合规化</span>
            <span class="gd-brand__tagline">
              多套排版规则可选 · 改动必须显式声明、可见、可审计
            </span>
          </span>
        </div>
        <div class="gd-header__actions">
          <span class="gd-badge" :class="statusBadge.cls">
            <span class="gd-dot" aria-hidden="true"></span>
            {{ statusBadge.text }}
          </span>
          <span v-if="gov.upload" class="gd-badge gd-badge--accent">
            {{ gov.upload.source_name }}
          </span>
          <!--
            顶部常驻的「下载公文文档」。
            用户的原话是「少了个最终下载文档的按钮」，因此只要文件已生成且方案
            仍通过校验，这个按钮就出现在页头 —— 无论用户停在哪一区都能一步下载。
          -->
          <button
            v-if="hasDownload"
            type="button"
            class="gd-btn gd-btn--sm gd-btn--primary"
            @click="downloadRenderedFile"
          >
            下载公文文档
          </button>
        </div>
      </div>
    </header>

    <!-- 一次性提示 -->
    <div v-if="gov.notice" class="gd-toast">
      <div class="gd-toast__inner" :data-kind="noticeKind" role="status">
        <span aria-hidden="true">
          <template v-if="noticeKind === 'error'">!</template>
          <template v-else-if="noticeKind === 'ok'">✓</template>
          <template v-else>i</template>
        </span>
        <span class="gd-grow">{{ gov.notice.text }}</span>
        <button type="button" class="gd-mini" title="关闭提示" @click="dismissNotice">✕</button>
      </div>
    </div>

    <div class="gd-shell gd-main">
      <!-- 左侧导航 -->
      <nav class="gd-nav" aria-label="功能区">
        <div class="gd-nav__list">
          <button
            v-for="n in navItems"
            :key="n.key"
            type="button"
            class="gd-nav__item"
            :aria-current="gov.tab === n.key ? 'page' : undefined"
            :disabled="!n.enabled"
            :title="n.enabled ? '' : n.sub"
            @click="go(n.key)"
          >
            <span class="gd-nav__index" aria-hidden="true">{{ n.index }}</span>
            <span class="gd-nav__text">
              <span>{{ n.label }}</span>
              <span class="gd-nav__sub">{{ n.sub }}</span>
            </span>
          </button>
        </div>

        <div class="gd-card gd-card--compact gd-card--secondary">
          <div class="gd-card__title">如何开始</div>
          <ol class="gd-stack gd-stack--tight" style="font-size: 12px; color: var(--muted)">
            <li>1. 上传 .docx / .txt / .md 文件</li>
            <li>2. 填写模型接口、名称与 API Key</li>
            <li>3. 点击「开始分析」等待进度</li>
            <li>4. 校验通过后点「生成公文文档」</li>
            <li>5. 点「下载公文文档」拿到 .docx</li>
          </ol>
          <div class="gd-field__hint">
            第 4、5 步都在「分析与生成」区，无需人工校对。想逐段核对原文或审阅模型的
            改动时，再进「高级校对（可选）」。
          </div>
        </div>

        <div v-if="gov.bootError" class="gd-alert gd-alert--danger">
          <span class="gd-alert__icon" aria-hidden="true">!</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">无法连接后端服务</div>
            <div>{{ gov.bootError }}</div>
            <div class="gd-alert__list">
              请确认 govdoc 服务已启动，且当前页面地址与服务同源。
            </div>
          </div>
        </div>
      </nav>

      <!-- 工作区 -->
      <main>
        <div v-if="gov.booting" class="gd-card">
          <div class="gd-stack">
            <div class="gd-skeleton" style="width: 40%"></div>
            <div class="gd-skeleton" style="width: 70%"></div>
            <div class="gd-skeleton" style="width: 55%"></div>
          </div>
        </div>

        <PanelUpload v-else-if="gov.tab === 'upload'" @done="go('analyze')" />
        <PanelAnalyze v-else-if="gov.tab === 'analyze'" @review="go('review')" />
        <PanelReview v-else-if="gov.tab === 'review'" @preview="go('preview')" />
        <PanelSettings v-else-if="gov.tab === 'settings'" />
        <PanelPreview v-else />
      </main>
    </div>

    <footer class="gd-footer">
      <div class="gd-shell gd-footer__inner">
        <span>
          核心保证：任何改动都必须显式声明（before → after + 理由），界面上逐条可见、可拒绝；
          未声明的改写会让导出失败，由 VerifyVerbatim 逐字比对把关。
        </span>
        <span v-if="currentProfileName">
          当前文档排版规则：{{ currentProfileName }}
        </span>
      </div>
    </footer>
  </div>
</template>
