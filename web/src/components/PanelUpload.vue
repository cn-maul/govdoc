<script setup lang="ts">
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
import { computed, ref } from 'vue'
import { KIND_LABEL, specSummary, type NodeSummary } from '../api/types'
import {
  currentProfileName,
  gov,
  loadSource,
  notify,
  uploadDocument,
} from '../composables/useGovdoc'

const emit = defineEmits<{ (e: 'done'): void }>()

// ---------------------------------------------------------------- 上传

const inputRef = ref<HTMLInputElement | null>(null)
const dragging = ref(false)

/** 允许的扩展名与后端 internal/parse 支持的一致。 */
const ACCEPT = '.docx,.txt,.md'

function pickFile(): void {
  inputRef.value?.click()
}

function onPicked(ev: Event): void {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  if (file) void beginUpload(file)
  // 允许重复选择同一个文件。
  input.value = ''
}

function onDrop(ev: DragEvent): void {
  dragging.value = false
  const file = ev.dataTransfer?.files?.[0]
  if (!file) {
    notify('error', '没有检测到文件，请重新拖入或点击选择。')
    return
  }
  void beginUpload(file)
}

function onDragOver(ev: DragEvent): void {
  ev.preventDefault()
  dragging.value = true
}

function onDragLeave(): void {
  dragging.value = false
}

async function beginUpload(file: File): Promise<void> {
  const ok = await uploadDocument(file, gov.selectedProfile)
  if (ok) {
    // 顺带把原文读回来，校对区左栏才能立刻逐字显示。
    if (gov.upload) void loadSource(gov.upload.doc_id)
  }
}

// ---------------------------------------------------------------- 排版规则

const profiles = computed(() => gov.profiles)

/** 选中的那套规则的摘要，随选择即时变化。 */
const selectedSummary = computed(() => {
  const p = profiles.value.find((x) => x.id === gov.selectedProfile)
  return p ? specSummary(p.spec) : ''
})

const nodeStats = computed(() => {
  const nodes: NodeSummary[] = gov.upload?.nodes ?? []
  const byKind = new Map<string, number>()
  for (const n of nodes) byKind.set(n.kind, (byKind.get(n.kind) ?? 0) + 1)
  return [...byKind.entries()].map(([kind, count]) => ({
    kind,
    label: KIND_LABEL[kind as NodeSummary['kind']] ?? kind,
    count,
  }))
})
</script>

<template>
  <section class="gd-work">
    <header class="gd-section-title">
      <h2>上传文档</h2>
      <p>选择源文件与排版规则。模型连接与更多规则可在「设置」页调整。</p>
    </header>

    <div class="gd-grid-2">
      <!-- 排版规则 -->
      <div class="gd-card">
        <div class="gd-card__header">
          <div class="gd-card__headings">
            <div class="gd-card__title">排版规则</div>
            <div class="gd-card__description">
              决定这份文档的字体、字号、行距与页边距。公文只是其中一套。
            </div>
          </div>
          <span v-if="profiles.length" class="gd-badge gd-badge--accent">
            {{ profiles.length }} 套可选
          </span>
        </div>

        <div class="gd-field">
          <label class="gd-label" for="pick-profile">使用哪套配置</label>
          <select id="pick-profile" v-model="gov.selectedProfile" class="gd-select">
            <option v-for="p in profiles" :key="p.id" :value="p.id">
              {{ p.name }}{{ p.id === gov.activeProfile ? '（默认）' : '' }}
            </option>
          </select>
          <div v-if="selectedSummary" class="gd-field__hint">{{ selectedSummary }}</div>
          <div class="gd-field__hint">
            需要更多？到「设置 → 排版规则」可以新建、复制并逐项调整字体字号。
          </div>
        </div>

        <div v-if="gov.upload" class="gd-alert gd-alert--info">
          <span class="gd-alert__icon" aria-hidden="true">i</span>
          <div class="gd-alert__body">
            <div class="gd-alert__title">本文档已锁定使用「{{ currentProfileName }}」</div>
            <div>
              规则在上传时确定，之后修改默认配置不会影响这份文档。想换规则，重新上传一次即可。
            </div>
          </div>
        </div>
      </div>

      <!-- 上传 -->
      <div class="gd-card">
        <div class="gd-card__header">
          <div class="gd-card__headings">
            <div class="gd-card__title">源文件</div>
            <div class="gd-card__description">支持 .docx / .txt / .md，原文将逐字保留。</div>
          </div>
          <span v-if="gov.upload" class="gd-badge gd-badge--success">{{ gov.upload.format }}</span>
        </div>

        <div
          class="gd-drop"
          :data-active="dragging"
          role="button"
          tabindex="0"
          @click="pickFile"
          @keydown.enter.prevent="pickFile"
          @keydown.space.prevent="pickFile"
          @dragover="onDragOver"
          @dragleave="onDragLeave"
          @drop.prevent="onDrop"
        >
          <div class="gd-drop__title">
            <template v-if="gov.uploading">
              <span class="gd-spinner" aria-hidden="true"></span> 正在解析文件…
            </template>
            <template v-else-if="gov.upload">已上传「{{ gov.upload.source_name }}」</template>
            <template v-else>把文件拖到这里，或点击选择</template>
          </div>
          <div class="gd-drop__hint">单文件，解析后可在校对区逐段核对原文。</div>
          <input
            ref="inputRef"
            class="gd-visually-hidden"
            type="file"
            :accept="ACCEPT"
            @change="onPicked"
          />
        </div>

        <div v-if="gov.upload" class="gd-stack gd-stack--tight">
          <div class="gd-row gd-row--between">
            <span class="gd-muted" style="font-size: 13px">
              共 {{ gov.upload.node_count }} 个块，其中正文 {{ gov.upload.content_count }} 段
            </span>
            <button type="button" class="gd-btn gd-btn--sm gd-btn--ghost" @click="pickFile">
              更换文件
            </button>
          </div>
          <div class="gd-row">
            <span v-for="s in nodeStats" :key="s.kind" class="gd-badge">
              {{ s.label }} {{ s.count }}
            </span>
          </div>
        </div>
      </div>
    </div>

    <div class="gd-row gd-row--end">
      <button
        type="button"
        class="gd-btn gd-btn--primary gd-btn--lg"
        :disabled="!gov.upload"
        @click="emit('done')"
      >
        下一步：分析文档结构
      </button>
    </div>
  </section>
</template>