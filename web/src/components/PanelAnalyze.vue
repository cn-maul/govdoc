<script setup lang="ts">
/**
 * 分析区。
 *
 * 点「开始分析」→ POST /api/documents/{id}/plan 拿 job_id → EventSource 订阅
 * GET /api/jobs/{job_id}/events，实时显示中文进度；结束后展示 plan 与 report，
 * 并把 report 的诊断列成列表（error 红 / warning 黄 / info 灰）。
 */
import { computed, ref, watch } from 'vue'
import {
  SEVERITY_LABEL,
  editLabel,
  reportOK,
  reportSummary,
  type Diagnostic,
  type Severity,
} from '../api/types'
import { diagnosticsOf, issueCounts } from '../lib/report'
import {
  canRender,
  downloadBlockReason,
  gov,
  renderDocument,
  startAnalysis,
} from '../composables/useGovdoc'

const emit = defineEmits<{ (e: 'review'): void }>()

const hint = ref('')
/** 只显示某一级别的诊断；null 表示全部。 */
const filter = ref<Severity | null>(null)

/**
 * 是否允许模型修正错别字/语病/用词。
 *
 * 初值取服务端保存的偏好（配置区里设过就沿用），用户可以在本次分析上临时改。
 */
const allowEdits = ref(false)

const docId = computed(() => gov.upload?.doc_id ?? '')
const llmReady = computed(() => gov.health?.llm_configured ?? false)

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

const stageRows = computed<StageRow[]>(() => {
  const seen = new Map(gov.stages.map((s) => [s.name, s]))
  const names = STAGE_ORDER.filter((n) => seen.has(n) || gov.analyzing)
  for (const s of gov.stages) if (!names.includes(s.name)) names.push(s.name)

  const last = gov.stages[gov.stages.length - 1]
  return names.map((name) => {
    const s = seen.get(name)
    return {
      name,
      label: STAGE_LABEL[name] ?? name,
      message: s?.message ?? (gov.analyzing ? '等待中…' : '未执行'),
      done: s?.done ?? false,
      active: gov.analyzing && last?.name === name && !s?.done,
    }
  })
})

const counts = computed(() => issueCounts(gov.report))

const visibleDiagnostics = computed<Diagnostic[]>(() => {
  if (filter.value === null) return gov.report?.diagnostics ?? []
  return diagnosticsOf(gov.report, filter.value)
})

const ok = computed(() => reportOK(gov.report))

const coveragePct = computed(() => {
  const r = gov.report
  if (!r || r.content_nodes === 0) return 0
  return Math.round((r.covered_nodes / r.content_nodes) * 100)
})

watch(
  () => docId.value,
  () => {
    hint.value = ''
    filter.value = null
    // 换文档时回到服务端保存的偏好，而不是沿用上一份文档的临时选择。
    allowEdits.value = gov.config?.allow_edits ?? false
  },
)

watch(
  () => gov.config?.allow_edits,
  (v) => {
    if (v !== undefined) allowEdits.value = v
  },
  { immediate: true },
)

async function analyze(): Promise<void> {
  if (docId.value === '') return
  if (!llmReady.value) {
    // 仍然允许尝试，但先说清后果：服务端会返回 llm_not_configured。
    hint.value = hint.value // 保留用户输入
  }
  await startAnalysis(docId.value, hint.value, undefined, allowEdits.value)
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
const readyToRender = computed(() => reportOK(gov.report) && gov.plan !== null)
const downloadReady = computed(() => gov.render !== null && reportOK(gov.report))
const downloadHint = computed(() => downloadBlockReason.value)

async function generate(): Promise<void> {
  await renderDocument(docId.value)
}

/** 本次方案声明的改动条数与类型摘要，让用户知道模型到底动了什么。 */
const editSummary = computed(() => {
  const edits = gov.plan?.edits ?? []
  const labels = new Map<string, number>()
  for (const e of edits) labels.set(e.kind, (labels.get(e.kind) ?? 0) + 1)
  return {
    total: edits.length,
    parts: [...labels.entries()].map(([k, n]) => `${editLabel(k)} ${n}`),
  }
})

/** 严重级别的显示名。 */
function sevLabel(s: Severity): string {
  return SEVERITY_LABEL[s]
}
</script>

<template>
  <section class="gd-work">
    <header class="gd-section-title">
      <h2>分析文档结构</h2>
      <p>由模型梳理章节层次，产出方案后逐项校验是否漏收或重复了原文。</p>
    </header>

    <div v-if="!gov.upload" class="gd-card">
      <div class="gd-empty">
        <div>还没有上传文档。</div>
        <div class="gd-muted">请先在「上传与配置」区选择一份 .docx / .txt / .md 文件。</div>
      </div>
    </div>

    <template v-else>
      <div v-if="!llmReady" class="gd-alert gd-alert--warning">
        <span class="gd-alert__icon" aria-hidden="true">!</span>
        <div class="gd-alert__body">
          <div class="gd-alert__title">尚未配置模型</div>
          <div>
            开始分析前请先填写接口地址、模型名称与 API Key。若直接发起，服务端会返回「未配置模型」。
          </div>
        </div>
      </div>

      <!-- 启动 + 进度 -->
      <div class="gd-card">
        <div class="gd-card__header">
          <div class="gd-card__headings">
            <div class="gd-card__title">分析进度</div>
            <div class="gd-card__description">
              文档「{{ gov.upload.source_name }}」，正文 {{ gov.upload.content_count }} 段。
            </div>
          </div>
          <span v-if="gov.analyzing" class="gd-badge gd-badge--accent">
            <span class="gd-dot gd-dot--pulse" aria-hidden="true"></span> 进行中
          </span>
          <span v-else-if="gov.plan" class="gd-badge" :class="ok ? 'gd-badge--success' : 'gd-badge--danger'">
            {{ ok ? '校验通过' : '校验未通过' }}
          </span>
        </div>

        <div class="gd-field">
          <label class="gd-label" for="plan-hint">
            补充说明
            <span class="gd-label__optional">可选</span>
          </label>
          <input
            id="plan-hint"
            v-model="hint"
            class="gd-input"
            type="text"
            placeholder="例如：这是一份通知，请按通报格式梳理"
            :disabled="gov.analyzing"
            autocomplete="off"
          />
          <div class="gd-field__hint">
            告诉模型文种或特殊要求，可提高章节切分的准确度；不影响原文内容。
          </div>
        </div>

        <div class="gd-field">
          <label class="gd-check">
            <input v-model="allowEdits" type="checkbox" :disabled="gov.analyzing" />
            <span>
              <span class="gd-label">允许模型修正错别字、语病与用词</span>
              <span class="gd-field__hint">
                默认关闭：只梳理结构，一个字都不动。开启后模型可以提出修正，但每一处
                修正都必须声明「原文什么 → 改成什么 → 为什么」，改动会逐条列出，
                可接受也可拒绝；<strong>未声明的改写仍会导致导出失败</strong>。
              </span>
            </span>
          </label>
        </div>

        <div v-if="stageRows.length > 0" class="gd-steps">
          <div
            v-for="s in stageRows"
            :key="s.name"
            class="gd-step"
            :class="{ 'gd-step--done': s.done, 'gd-step--active': s.active }"
          >
            <span class="gd-step__marker" aria-hidden="true">
              <template v-if="s.done">✓</template>
              <template v-else-if="s.active"><span class="gd-spinner"></span></template>
              <template v-else>·</template>
            </span>
            <span class="gd-step__text">{{ s.message }}</span>
          </div>
        </div>

        <div v-if="gov.analyzing" class="gd-progress" role="progressbar" aria-label="分析进行中">
          <div class="gd-progress__bar"></div>
        </div>

        <div v-if="gov.analysisError" class="gd-alert" :class="gov.plan ? 'gd-alert--warning' : 'gd-alert--danger'">
          <span class="gd-alert__icon" aria-hidden="true">!</span>
          <div class="gd-alert__body">
            <!-- 服务端在自修复失败时会连**部分方案**一起返回；此时方案是可用的，
                 只是需要人工修复，措辞必须区别于「什么都没拿到」。 -->
            <div class="gd-alert__title">
              {{ gov.plan ? '方案已生成，但未通过自动校验' : '分析未完成' }}
            </div>
            <div>{{ gov.analysisError }}</div>
            <div v-if="gov.plan" class="gd-alert__list">
              已保留模型给出的方案。请到「校对与输出」区修复漏收/重复后导出。
            </div>
          </div>
        </div>

        <div class="gd-card__footer">
          <button
            type="button"
            class="gd-btn gd-btn--primary"
            :disabled="gov.analyzing || docId === ''"
            @click="analyze"
          >
            <span v-if="gov.analyzing" class="gd-spinner" aria-hidden="true"></span>
            {{ gov.analyzing ? '分析中…' : gov.plan ? '重新分析' : '开始分析' }}
          </button>
        </div>
      </div>

      <!--
        傻瓜式主行动。
        校验一旦通过，这里就是整页最显眼的东西：用户不需要先「进入校对区」，
        直接一步生成并下载。校对区因此降级为可选的高级功能（下方显式的次级入口）。
      -->
      <div v-if="readyToRender" class="gd-card gd-action" :class="{ 'gd-action--ready': downloadReady }">
        <div class="gd-action__body">
          <div class="gd-action__icon" aria-hidden="true">
            <template v-if="downloadReady">✓</template>
            <template v-else>↓</template>
          </div>
          <div class="gd-action__text">
            <div class="gd-action__title">
              {{ downloadReady ? '公文文档已生成，可以直接下载' : '校验通过，可以生成公文文档了' }}
            </div>
            <div class="gd-action__sub">
              <template v-if="downloadReady">
                已生成「{{ gov.render?.filename }}」。若浏览器没有自动开始下载，点右侧按钮即可；
                按钮会一直留着，随时可以再下一次。
              </template>
              <template v-else>
                结构方案已通过校验，正文逐字对应原文，可以直接成文；无需再做人工校对。
              </template>
            </div>
            <div v-if="editSummary.total > 0" class="gd-row">
              <span class="gd-badge gd-badge--warning">已声明改动 {{ editSummary.total }} 处</span>
              <span v-for="p in editSummary.parts" :key="p" class="gd-badge">{{ p }}</span>
            </div>
          </div>
        </div>

        <div class="gd-action__buttons">
          <!-- 已有文件时，「下载公文文档」就是主按钮；否则先「生成公文文档」。 -->
          <a
            v-if="downloadReady"
            class="gd-btn gd-btn--primary gd-btn--lg"
            :href="`api/files/${gov.render?.file_id}`"
            download
          >
            下载公文文档
          </a>
          <button
            v-else
            type="button"
            class="gd-btn gd-btn--primary gd-btn--lg"
            :disabled="!canRender"
            :title="canRender ? '' : '方案有未提交的修改或校验未通过'"
            @click="generate"
          >
            <span v-if="gov.rendering" class="gd-spinner" aria-hidden="true"></span>
            {{ gov.rendering ? '生成中…' : '生成公文文档' }}
          </button>

          <button
            v-if="downloadReady"
            type="button"
            class="gd-btn gd-btn--secondary"
            :disabled="gov.rendering"
            @click="generate"
          >
            重新生成
          </button>
        </div>

        <div v-if="downloadHint !== ''" class="gd-field__hint">{{ downloadHint }}</div>
      </div>

      <!-- 校验未通过：说清下一步仍是「生成」，只是需要先修掉问题 -->
      <div v-else-if="gov.plan && gov.report" class="gd-card gd-card--compact gd-card--secondary">
        <div class="gd-row gd-row--between">
          <span class="gd-muted" style="font-size: 13px">
            方案尚未通过校验，暂时无法成文。多数情况可以一键修好，也可以手动逐条处理。
          </span>
          <div class="gd-row">
            <button type="button" class="gd-btn gd-btn--sm gd-btn--secondary" @click="emit('review')">
              打开高级校对
            </button>
          </div>
        </div>
      </div>

      <!-- 校验结论 -->
      <div v-if="gov.report" class="gd-card">
        <div class="gd-card__header">
          <div class="gd-card__headings">
            <div class="gd-card__title">校验结论</div>
            <div class="gd-card__description">{{ reportSummary(gov.report) }}</div>
          </div>
        </div>

        <div class="gd-grid-4">
          <div class="gd-stat" :class="ok ? 'gd-stat--success' : 'gd-stat--danger'">
            <div class="gd-stat__value">{{ gov.report.covered_nodes }}/{{ gov.report.content_nodes }}</div>
            <div class="gd-stat__label">正文节点已覆盖</div>
          </div>
          <div class="gd-stat" :class="gov.report.duplicated_nodes > 0 ? 'gd-stat--danger' : ''">
            <div class="gd-stat__value">{{ gov.report.duplicated_nodes }}</div>
            <div class="gd-stat__label">重复收录</div>
          </div>
          <div class="gd-stat" :class="gov.report.unknown_refs > 0 ? 'gd-stat--danger' : ''">
            <div class="gd-stat__value">{{ gov.report.unknown_refs }}</div>
            <div class="gd-stat__label">无效引用</div>
          </div>
          <div class="gd-stat">
            <div class="gd-stat__value">{{ coveragePct }}%</div>
            <div class="gd-stat__label">覆盖率</div>
          </div>
        </div>

        <div class="gd-row">
          <span class="gd-badge gd-badge--danger">错误 {{ counts.error }}</span>
          <span class="gd-badge gd-badge--warning">警告 {{ counts.warning }}</span>
          <span class="gd-badge">提示 {{ counts.info }}</span>
          <span class="gd-badge" v-if="gov.report.synthesized_headings > 0">
            模型自拟标题 {{ gov.report.synthesized_headings }}
          </span>
          <span class="gd-badge" v-if="gov.report.reordered_sections > 0">
            顺序被调整 {{ gov.report.reordered_sections }} 节
          </span>
          <span class="gd-badge" v-if="gov.report.bound_elements > 0">
            已绑定公文要素 {{ gov.report.bound_elements }}
          </span>
        </div>

        <div class="gd-filter">
          <button
            type="button"
            class="gd-segment__item"
            :aria-pressed="filter === null"
            @click="filter = null"
          >
            全部 {{ (gov.report.diagnostics ?? []).length }}
          </button>
          <button
            v-for="s in (['error', 'warning', 'info'] as Severity[])"
            :key="s"
            type="button"
            class="gd-segment__item"
            :aria-pressed="filter === s"
            @click="filter = s"
          >
            {{ sevLabel(s) }} {{ counts[s] }}
          </button>
        </div>

        <div v-if="visibleDiagnostics.length > 0" class="gd-diag-list">
          <div
            v-for="(d, i) in visibleDiagnostics"
            :key="`${d.code}-${i}`"
            class="gd-diag"
            :class="`gd-diag--${d.severity}`"
          >
            <span class="gd-diag__code">{{ d.code }}</span>
            <span class="gd-diag__message">{{ d.message }}</span>
          </div>
        </div>
        <div v-else class="gd-empty">
          <div>{{ filter === null ? '没有任何诊断，方案与原文完全对应。' : '该级别下没有问题。' }}</div>
        </div>

        <div class="gd-card__footer">
          <span class="gd-muted" style="font-size: 12px">
            以上为技术性校验明细；通过校验时无需理会，可直接在上方生成并下载。
          </span>
          <button type="button" class="gd-btn gd-btn--ghost" @click="emit('review')">
            打开高级校对（可选）
          </button>
        </div>
      </div>
    </template>
  </section>
</template>
