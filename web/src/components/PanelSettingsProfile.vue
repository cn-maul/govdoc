<script setup lang="ts">
/**
 * 排版规则（profile）管理。
 *
 * 软件因此是通用的：公文只是其中一套内置规则，换一套就换掉标题/正文/行间距/
 * 字体/字号/页边距 —— 渲染管线不变，变的只是这张规则表。
 *
 * 关于「内置不可改」：与其让用户点了再吃一个 400，不如把按钮**禁用**并把原因
 * 写在旁边。内置规则是所有文档的兜底，也是恢复标准样式的唯一依据，所以真正
 * 该做的不是解禁，而是把「复制一份」放到最显眼的位置。
 */
import { computed, ref } from 'vue'
import { specSummary, type Profile, type Spec } from '../api/types'
import SpecEditor from './SpecEditor.vue'
import { canDelete, cloneSpec, isBuiltin, suggestCopyName, validateSpec } from '../lib/specEdit'
import {
  copyProfile,
  deleteProfile,
  gov,
  notify,
  refreshProfiles,
  saveProfile,
  setActiveProfile,
} from '../composables/useGovdoc'

/** 正在编辑的 profile id；null 表示没在编辑。 */
const editingId = ref<string | null>(null)
/** 编辑中的规则副本（改动先落在这里，点保存才提交）。 */
const draft = ref<Spec | null>(null)
/** 编辑中的名称与说明。 */
const draftName = ref('')
const draftDesc = ref('')

const list = computed(() => gov.profiles)
const activeId = computed(() => gov.activeProfile)

const editing = computed<Profile | null>(
  () => list.value.find((p) => p.id === editingId.value) ?? null,
)

function openEditor(p: Profile): void {
  editingId.value = p.id
  draft.value = cloneSpec(p.spec)
  draftName.value = p.name
  draftDesc.value = p.description ?? ''
}

function closeEditor(): void {
  editingId.value = null
  draft.value = null
  draftName.value = ''
  draftDesc.value = ''
}

function onDraftChange(next: Spec): void {
  draft.value = next
}

const draftProblems = computed(() => (draft.value ? validateSpec(draft.value) : []))

async function onSaveDraft(): Promise<void> {
  if (!editing.value || !draft.value) return
  const ok = await saveProfile({
    id: editing.value.id,
    name: draftName.value.trim() || editing.value.name,
    description: draftDesc.value.trim(),
    spec: draft.value,
  })
  if (ok) closeEditor()
}

/** 新建一份空白配置：从「公文」起手，而不是从零。 */
const newName = ref('')

async function onCreateBlank(): Promise<void> {
  const name = newName.value.trim()
  if (name === '') {
    notify('error', '请先给新配置起个名字。')
    return
  }
  const created = await copyProfile('govdoc', name, '基于公文规则新建')
  newName.value = ''
  if (created) openEditor(created)
}

function onCopy(p: Profile): void {
  newName.value = suggestCopyName(p.name)
  void copyProfile(p.id, newName.value, p.description ?? '').then((c) => {
    if (c) {
      newName.value = ''
      openEditor(c)
    }
  })
}

async function onActivate(p: Profile): Promise<void> {
  await setActiveProfile(p.id)
  // 让「哪套是默认」立刻反映到上传区的选择上。
  gov.selectedProfile = p.id
}

async function onDelete(p: Profile): Promise<void> {
  const gate = canDelete(p, activeId.value)
  if (!gate.ok) {
    notify('error', gate.why)
    return
  }
  if (!window.confirm(`确定删除「${p.name}」吗？此操作不可撤销。`)) return
  if (editingId.value === p.id) closeEditor()
  await deleteProfile(p.id)
}

async function onReload(): Promise<void> {
  closeEditor()
  await refreshProfiles()
}
</script>

<template>
  <div class="gd-card">
    <div class="gd-card__header">
      <div class="gd-card__headings">
        <div class="gd-card__title">排版规则（配置）</div>
        <div class="gd-card__description">
          换一套规则就换掉标题、正文、行间距、字体、字号与页边距。已上传的文档各自记住
          当时用的规则，切换默认不会影响它们。
        </div>
      </div>
      <button
        type="button"
        class="gd-btn gd-btn--sm gd-btn--ghost"
        :disabled="gov.savingProfile"
        @click="onReload"
      >
        刷新
      </button>
    </div>

    <div class="gd-table-wrap">
      <table class="gd-table">
        <thead>
          <tr>
            <th>名称</th>
            <th>规则摘要</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in list" :key="p.id">
            <td>
              <div class="gd-row gd-row--tight">
                <strong>{{ p.name }}</strong>
                <span v-if="p.id === activeId" class="gd-badge gd-badge--accent">默认</span>
                <span v-if="isBuiltin(p)" class="gd-badge">内置</span>
              </div>
              <div v-if="p.description" class="gd-field__hint">{{ p.description }}</div>
            </td>
            <td class="gd-muted gd-cell--summary">{{ specSummary(p.spec) }}</td>
            <td>
              <div class="gd-row gd-row--tight">
                <button
                  v-if="!isBuiltin(p)"
                  type="button"
                  class="gd-btn gd-btn--sm"
                  @click="openEditor(p)"
                >
                  编辑
                </button>
                <button
                  type="button"
                  class="gd-btn gd-btn--sm gd-btn--ghost"
                  :disabled="gov.savingProfile"
                  @click="onCopy(p)"
                >
                  复制一份
                </button>
                <button
                  type="button"
                  class="gd-btn gd-btn--sm gd-btn--ghost"
                  :disabled="gov.savingProfile || p.id === activeId"
                  :title="canDelete(p, activeId).ok ? '删除这份配置' : canDelete(p, activeId).why"
                  @click="onDelete(p)"
                >
                  删除
                </button>
              </div>
            </td>
          </tr>
          <tr v-if="list.length === 0">
            <td colspan="3" class="gd-muted">尚未取得排版规则列表。</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="gd-field">
      <span class="gd-label">设为新文档的默认规则</span>
      <div class="gd-segment" role="group" aria-label="默认规则">
        <button
          v-for="p in list"
          :key="p.id"
          type="button"
          class="gd-segment__item"
          :aria-pressed="p.id === activeId"
          :disabled="gov.savingProfile"
          @click="onActivate(p)"
        >
          {{ p.name }}
        </button>
      </div>
      <div class="gd-field__hint">
        改动只影响**之后**上传的文档。已经上传的文档在上传时就记住了自己的规则，
        这样历史文档不会在你改默认值时被悄悄换一套版式。
      </div>
    </div>

    <div class="gd-field">
      <span class="gd-label">新建一份配置</span>
      <div class="gd-row gd-row--tight">
        <input
          v-model="newName"
          class="gd-input gd-grow"
          type="text"
          placeholder="例如：公司报告、投稿须知"
          autocomplete="off"
        />
        <button
          type="button"
          class="gd-btn gd-btn--primary"
          :disabled="gov.savingProfile || newName.trim() === ''"
          @click="onCreateBlank"
        >
          新建
        </button>
      </div>
      <div class="gd-field__hint">
        新配置从「公文」规则起步（这是最常用的起点），创建后立刻进入编辑，可随意调整。
      </div>
    </div>

    <!-- 编辑器 -->
    <div v-if="editing && draft" class="gd-editor">
      <div class="gd-row gd-row--between">
        <div>
          <div class="gd-card__title">编辑「{{ draftName || editing.name }}」</div>
          <div class="gd-field__hint">
            改动先落在这里，点「保存规则」才会写入配置；「放弃」丢弃本次修改。
          </div>
        </div>
      </div>

      <div class="gd-field-row">
        <div class="gd-field">
          <label class="gd-label">名称</label>
          <input v-model="draftName" class="gd-input" type="text" autocomplete="off" />
        </div>
        <div class="gd-field">
          <label class="gd-label">说明（可选）</label>
          <input v-model="draftDesc" class="gd-input" type="text" autocomplete="off" />
        </div>
      </div>

      <SpecEditor :spec="draft" :read-only="isBuiltin(editing)" @change="onDraftChange" />

      <div class="gd-row gd-row--end">
        <button type="button" class="gd-btn gd-btn--ghost" @click="closeEditor">放弃</button>
        <button
          type="button"
          class="gd-btn gd-btn--primary"
          :disabled="gov.savingProfile || draftProblems.length > 0"
          @click="onSaveDraft"
        >
          {{ gov.savingProfile ? '保存中…' : '保存规则' }}
        </button>
      </div>
    </div>
  </div>
</template>