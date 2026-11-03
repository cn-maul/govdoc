<script setup lang="ts">
/**
 * 分页预览区。
 *
 * 预览的 HTML 由 internal/render/preview 生成（服务端渲染），这里只负责把它
 * 装进 iframe。用 iframe 而不是 v-html 有三个理由：
 *   1. 预览自带 <style>，直接内联会污染整个应用的样式；
 *   2. iframe 的 sandbox 可以彻底禁止脚本，而预览内容包含用户上传的原文；
 *   3. 纸张尺寸（A4 210×297mm）与页面滚动由独立的文档上下文承担，缩放更可靠。
 *
 * 界面上的缩放只改变 iframe 的 CSS transform，不改纸张内部的国标度量。
 */
import { computed, ref, watch } from 'vue'
import { api } from '../api/client'
import { editLabel } from '../api/types'
import { currentProfileName, gov } from '../composables/useGovdoc'

const docId = computed(() => gov.upload?.doc_id ?? '')
const loading = ref(false)
const failed = ref('')
const zoom = ref(1)
/** 预览的缓存键：方案每次改动后都要重新拉，否则看到的还是旧纸。 */
const revision = ref(0)

/**
 * 方案里声明了改动时，被改到的块在预览里带 data-edited="1" 并有高亮。
 * 这里只是把该事实告诉用户 —— 高亮本身由服务端渲染的 HTML 决定，
 * 界面不重复判断「哪一段被改了」，否则两处判断一旦不一致就会互相矛盾。
 */
const declaredEdits = computed(() => gov.plan?.edits ?? [])

/** 按类型汇总声明的改动，用于图例旁的小结。 */
const declaredSummary = computed(() => {
  const map = new Map<string, number>()
  for (const e of declaredEdits.value) map.set(e.kind, (map.get(e.kind) ?? 0) + 1)
  return [...map.entries()].map(([k, n]) => `${editLabel(k)} ${n}`)
})

const src = computed(() => {
  if (docId.value === '') return ''
  // revision 参与 query，强制 iframe 在方案更新后重新加载。
  return `${api.previewURL(docId.value)}?v=${revision.value}`
})

watch(
  () => gov.render,
  () => {
    revision.value++
  },
)

watch(
  () => gov.report,
  () => {
    revision.value++
  },
)

// 方案里的改动一改（接受/拒绝/新增），预览就必须重新拉：高亮是按 plan.edits
// 算出来的，不重载的话用户会看到旧的高亮，从而对「哪些段落被改过」产生误判。
watch(
  () => gov.plan?.edits,
  () => {
    revision.value++
  },
  { deep: true },
)

function reload(): void {
  revision.value++
  loading.value = true
  failed.value = ''
}

function onLoad(): void {
  loading.value = false
  failed.value = ''
}

function onError(): void {
  loading.value = false
  failed.value = '无法加载预览。请确认后端服务正在运行，且文档仍然存在于服务端。'
}

const ZOOMS = [0.5, 0.75, 1, 1.25, 1.5]

const previewURLCopy = computed(() => (docId.value === '' ? '' : api.previewURL(docId.value)))

const specLine = computed(() => {
  const s = gov.specInfo?.spec
  if (!s) return ''
  return `A4 ${s.page.width_mm}×${s.page.height_mm}mm · 版心 156×225mm · 每面 ${s.page.lines_per_page} 行 × 每行 ${s.page.chars_per_line} 字`
})

const spacingLine = computed(() => {
  const p = gov.specInfo?.spec?.page
  if (!p) return ''
  const linePT = (p.height_mm - p.margin_top_mm - p.margin_bottom_mm) * (72 / 25.4) / p.lines_per_page
  const pitch = (p.width_mm - p.margin_left_mm - p.margin_right_mm) * (72 / 25.4) / p.chars_per_line
  return `固定行距 ${linePT.toFixed(2)}pt · 每字 ${pitch.toFixed(2)}pt`
})
</script>

<template>
  <section class="gd-work">
    <header class="gd-section-title">
      <h2>分页预览</h2>
      <p>
        按本文档所用的排版规则模拟分页。应用界面用设计令牌渲染，纸张内部严格使用
        规则的版面数值，两者不混用。
      </p>
    </header>

    <div v-if="docId === ''" class="gd-card">
      <div class="gd-empty">
        <div>还没有可预览的文档。</div>
        <div class="gd-muted">请先上传文档并生成结构方案。</div>
      </div>
    </div>

    <template v-else>
      <div class="gd-card gd-card--compact">
        <div class="gd-preview__toolbar">
          <div class="gd-row">
            <span v-if="currentProfileName" class="gd-badge gd-badge--accent">
              规则：{{ currentProfileName }}
            </span>
            <span class="gd-badge gd-badge--accent">
              {{ specLine }}
            </span>
            <span class="gd-badge">{{ spacingLine }}</span>
          </div>
          <div class="gd-row">
            <span class="gd-preview__hint">缩放</span>
            <div class="gd-segment" role="group" aria-label="预览缩放">
              <button
                v-for="z in ZOOMS"
                :key="z"
                type="button"
                class="gd-segment__item"
                :aria-pressed="zoom === z"
                @click="zoom = z"
              >
                {{ Math.round(z * 100) }}%
              </button>
            </div>
            <button type="button" class="gd-btn gd-btn--sm gd-btn--ghost" @click="reload">
              <span v-if="loading" class="gd-spinner" aria-hidden="true"></span>
              刷新
            </button>
            <a
              class="gd-btn gd-btn--sm gd-btn--secondary"
              :href="previewURLCopy"
              target="_blank"
              rel="noopener"
            >
              新窗口打开
            </a>
          </div>
        </div>
      </div>

      <div v-if="failed !== ''" class="gd-alert gd-alert--danger">
        <span class="gd-alert__icon" aria-hidden="true">!</span>
        <div class="gd-alert__body">
          <div class="gd-alert__title">预览加载失败</div>
          <div>{{ failed }}</div>
        </div>
      </div>

      <!--
        图例：纸张里的高亮由服务端渲染（块上带 data-edited="1"），
        这里在 iframe **外面**说明它是什么意思。iframe 内是用户上传的原文，
        不该由应用往里注入任何 UI，所以图例只能放在外面。
      -->
      <div class="gd-card gd-card--compact">
        <div class="gd-legend">
          <span class="gd-legend__item">
            <span class="gd-legend__swatch" aria-hidden="true"></span>
            高亮 = 该段含<strong>已声明</strong>的修改
          </span>
          <span class="gd-legend__item">
            <span class="gd-legend__swatch gd-legend__swatch--plain" aria-hidden="true"></span>
            无高亮 = 与原文逐字一致
          </span>
          <span v-if="declaredEdits.length > 0" class="gd-legend__item">
            <span class="gd-badge gd-badge--warning">声明改动 {{ declaredEdits.length }} 处</span>
            <span v-for="s in declaredSummary" :key="s" class="gd-badge">{{ s }}</span>
          </span>
          <span v-else class="gd-legend__item">
            <span class="gd-badge gd-badge--success">本方案未声明任何改动，全文逐字不变</span>
          </span>
        </div>
        <div class="gd-field__hint">
          高亮只用于屏幕上的复核提示，不改变字号、行距与页边距 ——
          纸张内部的度量始终来自本文档所用的排版规则，因此打印与成文不受影响。
          逐条改动的 before → after 清单见「高级校对」区的改动汇总面板。
        </div>
      </div>

      <div class="gd-preview">
        <!--
          缩放用 transform 实现，但 transform 不影响布局盒，所以外面套一层
          尺寸为「1/缩放」的容器：这样 50% 时不留空洞、150% 时滚动区域正确。
        -->
        <div class="gd-paper-stage">
          <div
            class="gd-paper-scaler"
            :style="{
              width: `${100 / zoom}%`,
              height: `calc(clamp(28rem, 78vh, 60rem) * ${1 / zoom})`,
            }"
          >
            <iframe
              class="gd-paper-frame"
              :src="src"
              :style="{ transform: `scale(${zoom})`, transformOrigin: 'top left' }"
              title="公文分页预览"
              sandbox="allow-same-origin"
              referrerpolicy="no-referrer"
              @load="onLoad"
              @error="onError"
            ></iframe>
          </div>
        </div>
        <div class="gd-preview__hint">
          预览为「一张白纸 + 投影」，四周灰底衬托。纸张内部的字号、行距、页边距与页数
          均由服务端按格式标准生成；若与成文后的 .docx 有差异，请以 .docx 为准并回报问题。
        </div>
      </div>
    </template>
  </section>
</template>
