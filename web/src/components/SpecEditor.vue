<script setup lang="ts">
/**
 * 单套排版规则的编辑器。
 *
 * 只负责「编辑一份规则表」这一件事：外面传进来一份 spec，改完 emit 出去。
 * 新建/复制/保存这些流程由父组件管，因为它们与服务端交互、涉及提示与列表刷新。
 *
 * 设计上的两个取舍：
 *
 * 1. 输入框绑定字符串而不是数字。用户删掉重打的过程里，绑 number 会立刻变成
 *    NaN 并把字段清零；中间留一层字符串，失焦或保存时才转数值。
 * 2. 字号是**下拉**而不是自由输入。中文排版是按字号名思考的（"正文用三号"），
 *    填磅值既不直观又容易输错，而服务端只认标准字号，自由输入注定被拒。
 */
import { computed, ref, watch } from 'vue'
import {
  EDITOR_ROLE_PRIMARY,
  FONT_CHOICES,
  FONT_SIZE_ORDER,
  FONT_SIZE_POINTS,
  ROLE_LABEL,
  type Align,
  type FontSize,
  type LineRule,
  type ParaStyle,
  type Role,
  type Spec,
} from '../api/types'
import {
  ALIGNS,
  cloneSpec,
  fromNumberText,
  LINE_RULES,
  pageSummary,
  styleSummary,
  validateSpec,
  withPage,
  withStyle,
} from '../lib/specEdit'

const props = defineProps<{
  spec: Spec
  /** 内置配置：只读，且给出为什么不能改。 */
  readOnly?: boolean
}>()

const emit = defineEmits<{ (e: 'change', spec: Spec): void }>()

/** 本地副本：不在 props 上就地改。 */
const local = ref<Spec>(cloneSpec(props.spec))

// 父组件换了一份规则（比如切换编辑对象）就重新拷贝。
watch(
  () => props.spec,
  (s) => {
    local.value = cloneSpec(s)
  },
  { deep: false },
)

function commit(next: Spec): void {
  if (props.readOnly) return
  local.value = next
  emit('change', next)
}

// ---------------------------------------------------------------- 展开状态

/** 默认展开标题、正文与前三级标题：用户调版式时真正会动的就是这几项。 */
const open = ref<Set<Role>>(new Set(EDITOR_ROLE_PRIMARY))
const showAdvanced = ref(false)

function toggle(role: Role): void {
  const s = new Set(open.value)
  if (s.has(role)) s.delete(role)
  else s.add(role)
  open.value = s
}

const problems = computed(() => validateSpec(local.value))
const problemsByRole = computed(() => {
  const m = new Map<string, string[]>()
  for (const p of problems.value) {
    const k = p.role
    const arr = m.get(k) ?? []
    arr.push(p.message)
    m.set(k, arr)
  }
  return m
})

// ---------------------------------------------------------------- 输入适配

/**
 * 数字输入框用字符串承载，失焦时转回数值写进 spec。
 *
 * 用户删掉重打的过程中，值会短暂地是空串；此时不能把它当成 0 立刻提交，
 * 否则用户清空后重新输入的数字会被前面的 0 盖掉，界面上还会闪一下 0。
 * 所以草稿按「字段路径」存在 drafts 里，键路径必须与 onXxxBlur 读的一致。
 */
const drafts = ref<Record<string, string>>({})

const draftKey = (path: string): string => path

/** 读取草稿值：没有草稿时显示当前值。 */
function draftValue(path: string, value: number): string {
  const d = drafts.value[path]
  if (d !== undefined) return d
  return Number.isFinite(value) ? String(Math.round(value * 1000) / 1000) : ''
}

/** 记录草稿值。 */
function onDraft(path: string, raw: string): void {
  drafts.value[draftKey(path)] = raw
}

/** 失焦时把草稿转成数值提交；空串按 0 处理，因为这些字段 0 是合法值。 */
function commitDraft(patch: (v: number) => Spec, path: string): void {
  const raw = drafts.value[path]
  if (raw === undefined) return
  const v = fromNumberText(raw)
  commit(patch(v ?? 0))
  delete drafts.value[path]
}

// ---------------------------------------------------------------- role 列表

interface StyleRow {
  role: Role
  label: string
  style: ParaStyle | undefined
}

const primaryRows = computed<StyleRow[]>(() =>
  EDITOR_ROLE_PRIMARY.map((r) => ({ role: r, label: ROLE_LABEL[r] ?? r, style: local.value.styles?.[r] })),
)

const otherRows = computed<StyleRow[]>(() => {
  const keys = Object.keys(local.value.styles ?? {}) as Role[]
  return keys
    .filter((r) => !EDITOR_ROLE_PRIMARY.includes(r))
    .map((r) => ({ role: r, label: ROLE_LABEL[r] ?? r, style: local.value.styles?.[r] }))
    .sort((a, b) => a.label.localeCompare(b.label, 'zh-Hans-CN'))
})
</script>

<template>
  <div class="gd-spec">
    <div v-if="readOnly" class="gd-alert gd-alert--info">
      <span class="gd-alert__icon" aria-hidden="true">i</span>
      <div class="gd-alert__body">
        <div class="gd-alert__title">这是内置配置，不能直接修改</div>
        <div>
          它是所有文档的兜底规则，也是恢复到标准样式的依据。关掉本页后点
          <strong>「复制一份」</strong>，就能在副本上随意调整，内置原件保持不变。
        </div>
      </div>
    </div>

    <div v-if="problems.length > 0" class="gd-alert gd-alert--warning">
      <span class="gd-alert__icon" aria-hidden="true">!</span>
      <div class="gd-alert__body">
        <div class="gd-alert__title">规则还有 {{ problems.length }} 处需要修正</div>
        <div class="gd-alert__list">
          <div v-for="(p, i) in problems" :key="i">· {{ p.message }}</div>
        </div>
      </div>
    </div>

    <!-- 页面 -->
    <section class="gd-spec__group">
      <h4 class="gd-spec__title">页面与页边距</h4>
      <div class="gd-field__hint">{{ pageSummary(local) }}</div>

      <div class="gd-grid-4">
        <div class="gd-field">
          <label class="gd-label">页宽 (mm)</label>
          <input
            class="gd-input"
            type="number"
            step="0.1"
            :disabled="readOnly"
            :value="draftValue('page.width_mm', local.page.width_mm)"
            @input="onDraft('page.width_mm', ($event.target as HTMLInputElement).value)"
            @blur="commitDraft((v) => withPage(local, { width_mm: v } as Partial<Spec['page']>), 'page.width_mm')"
          />
        </div>
        <div class="gd-field">
          <label class="gd-label">页高 (mm)</label>
          <input
            class="gd-input"
            type="number"
            step="0.1"
            :disabled="readOnly"
            :value="draftValue('page.height_mm', local.page.height_mm)"
            @input="onDraft('page.height_mm', ($event.target as HTMLInputElement).value)"
            @blur="commitDraft((v) => withPage(local, { height_mm: v } as Partial<Spec['page']>), 'page.height_mm')"
          />
        </div>
        <div class="gd-field">
          <label class="gd-label">上边距 (mm)</label>
          <input
            class="gd-input"
            type="number"
            step="0.1"
            :disabled="readOnly"
            :value="draftValue('page.margin_top_mm', local.page.margin_top_mm)"
            @input="onDraft('page.margin_top_mm', ($event.target as HTMLInputElement).value)"
            @blur="commitDraft((v) => withPage(local, { margin_top_mm: v } as Partial<Spec['page']>), 'page.margin_top_mm')"
          />
        </div>
        <div class="gd-field">
          <label class="gd-label">下边距 (mm)</label>
          <input
            class="gd-input"
            type="number"
            step="0.1"
            :disabled="readOnly"
            :value="draftValue('page.margin_bottom_mm', local.page.margin_bottom_mm)"
            @input="onDraft('page.margin_bottom_mm', ($event.target as HTMLInputElement).value)"
            @blur="commitDraft((v) => withPage(local, { margin_bottom_mm: v } as Partial<Spec['page']>), 'page.margin_bottom_mm')"
          />
        </div>
        <div class="gd-field">
          <label class="gd-label">左边距 (mm)</label>
          <input
            class="gd-input"
            type="number"
            step="0.1"
            :disabled="readOnly"
            :value="draftValue('page.margin_left_mm', local.page.margin_left_mm)"
            @input="onDraft('page.margin_left_mm', ($event.target as HTMLInputElement).value)"
            @blur="commitDraft((v) => withPage(local, { margin_left_mm: v } as Partial<Spec['page']>), 'page.margin_left_mm')"
          />
        </div>
        <div class="gd-field">
          <label class="gd-label">右边距 (mm)</label>
          <input
            class="gd-input"
            type="number"
            step="0.1"
            :disabled="readOnly"
            :value="draftValue('page.margin_right_mm', local.page.margin_right_mm)"
            @input="onDraft('page.margin_right_mm', ($event.target as HTMLInputElement).value)"
            @blur="commitDraft((v) => withPage(local, { margin_right_mm: v } as Partial<Spec['page']>), 'page.margin_right_mm')"
          />
        </div>
      </div>
    </section>

    <!-- 常用元素 -->
    <section class="gd-spec__group">
      <h4 class="gd-spec__title">主要元素</h4>
      <p class="gd-field__hint">
        标题、正文与各级小标题决定文档的主要观感。点标题展开可改字体、字号、行距与缩进。
      </p>

      <div v-for="row in primaryRows" :key="row.role" class="gd-spec__item">
        <button
          type="button"
          class="gd-spec__head"
          :aria-expanded="open.has(row.role)"
          :disabled="readOnly || !row.style"
          @click="toggle(row.role)"
        >
          <span class="gd-spec__caret" :data-open="open.has(row.role)" aria-hidden="true">▸</span>
          <span class="gd-spec__name">{{ row.label }}</span>
          <span class="gd-spec__sum">{{ styleSummary(row.style) }}</span>
        </button>

        <div v-if="open.has(row.role) && row.style" class="gd-spec__body">
          <div
            v-if="(problemsByRole.get(row.role) ?? []).length > 0"
            class="gd-alert gd-alert--warning"
          >
            <div class="gd-alert__body">
              <div v-for="(m, i) in problemsByRole.get(row.role)" :key="i" class="gd-alert__list">
                · {{ m }}
              </div>
            </div>
          </div>

          <div class="gd-grid-4">
            <div class="gd-field">
              <label class="gd-label">字体</label>
              <input
                class="gd-input"
                list="gd-font-list"
                type="text"
                :disabled="readOnly"
                :value="row.style.font"
                @input="commit(withStyle(local, row.role, { font: ($event.target as HTMLInputElement).value }))"
              />
            </div>
            <div class="gd-field">
              <label class="gd-label">字号</label>
              <select
                class="gd-select"
                :disabled="readOnly"
                :value="row.style.size"
                @change="commit(withStyle(local, row.role, { size: ($event.target as HTMLSelectElement).value as FontSize }))"
              >
                <option v-for="s in FONT_SIZE_ORDER" :key="s" :value="s">
                  {{ s }}（{{ FONT_SIZE_POINTS[s] }}pt）
                </option>
                <option v-if="!FONT_SIZE_ORDER.includes(row.style.size)" :value="row.style.size">
                  {{ row.style.size }}（非标准）
                </option>
              </select>
            </div>
            <div class="gd-field">
              <label class="gd-label">行距</label>
              <select
                class="gd-select"
                :disabled="readOnly"
                :value="row.style.line_rule"
                @change="commit(withStyle(local, row.role, { line_rule: ($event.target as HTMLSelectElement).value as LineRule }))"
              >
                <option v-for="r in LINE_RULES" :key="r.value" :value="r.value" :title="r.hint">
                  {{ r.label }}
                </option>
              </select>
            </div>
            <div class="gd-field">
              <label class="gd-label">行距 (pt)</label>
              <input
                class="gd-input"
                type="number"
                step="0.1"
                min="0"
                :disabled="readOnly || row.style.line_rule !== 'exact'"
                :value="draftValue(row.role + '.line_spacing_pt', row.style.line_spacing_pt)"
                @input="onDraft(row.role + '.line_spacing_pt', ($event.target as HTMLInputElement).value)"
                @blur="commitDraft((v) => withStyle(local, row.role, { line_spacing_pt: v } as Partial<ParaStyle>), row.role + '.line_spacing_pt')"
              />
            </div>
            <div class="gd-field">
              <label class="gd-label">首行缩进 (字)</label>
              <input
                class="gd-input"
                type="number"
                step="0.5"
                min="0"
                :disabled="readOnly"
                :value="draftValue(row.role + '.first_line_indent_chars', row.style.first_line_indent_chars)"
                @input="onDraft(row.role + '.first_line_indent_chars', ($event.target as HTMLInputElement).value)"
                @blur="commitDraft((v) => withStyle(local, row.role, { first_line_indent_chars: v } as Partial<ParaStyle>), row.role + '.first_line_indent_chars')"
              />
            </div>
            <div class="gd-field">
              <label class="gd-label">对齐</label>
              <select
                class="gd-select"
                :disabled="readOnly"
                :value="row.style.align"
                @change="commit(withStyle(local, row.role, { align: ($event.target as HTMLSelectElement).value as Align }))"
              >
                <option v-for="a in ALIGNS" :key="a.value" :value="a.value">{{ a.label }}</option>
              </select>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- 其余元素 -->
    <section class="gd-spec__group">
      <button type="button" class="gd-spec__head gd-spec__head--flat" @click="showAdvanced = !showAdvanced">
        <span class="gd-spec__caret" :data-open="showAdvanced" aria-hidden="true">▸</span>
        <span class="gd-spec__name">其余元素（{{ otherRows.length }} 项）</span>
        <span class="gd-spec__sum">版头、署名、日期、抄送等</span>
      </button>

      <div v-if="showAdvanced" class="gd-spec__body">
        <div v-for="row in otherRows" :key="row.role" class="gd-spec__item">
          <button
            type="button"
            class="gd-spec__head"
            :aria-expanded="open.has(row.role)"
            :disabled="readOnly || !row.style"
            @click="toggle(row.role)"
          >
            <span class="gd-spec__caret" :data-open="open.has(row.role)" aria-hidden="true">▸</span>
            <span class="gd-spec__name">{{ row.label }}</span>
            <span class="gd-spec__sum">{{ styleSummary(row.style) }}</span>
          </button>
          <div v-if="open.has(row.role) && row.style" class="gd-spec__body">
            <div class="gd-grid-4">
              <div class="gd-field">
                <label class="gd-label">字体</label>
                <input
                  class="gd-input"
                  list="gd-font-list"
                  type="text"
                  :disabled="readOnly"
                  :value="row.style.font"
                  @input="commit(withStyle(local, row.role, { font: ($event.target as HTMLInputElement).value }))"
                />
              </div>
              <div class="gd-field">
                <label class="gd-label">字号</label>
                <select
                  class="gd-select"
                  :disabled="readOnly"
                  :value="row.style.size"
                  @change="commit(withStyle(local, row.role, { size: ($event.target as HTMLSelectElement).value as FontSize }))"
                >
                  <option v-for="s in FONT_SIZE_ORDER" :key="s" :value="s">
                    {{ s }}（{{ FONT_SIZE_POINTS[s] }}pt）
                  </option>
                  <option v-if="!FONT_SIZE_ORDER.includes(row.style.size)" :value="row.style.size">
                    {{ row.style.size }}（非标准）
                  </option>
                </select>
              </div>
              <div class="gd-field">
                <label class="gd-label">行距 (pt)</label>
                <input
                  class="gd-input"
                  type="number"
                  step="0.1"
                  min="0"
                  :disabled="readOnly"
                  :value="draftValue(row.role + '.line_spacing_pt', row.style.line_spacing_pt)"
                  @input="onDraft(row.role + '.line_spacing_pt', ($event.target as HTMLInputElement).value)"
                  @blur="commitDraft((v) => withStyle(local, row.role, { line_spacing_pt: v } as Partial<ParaStyle>), row.role + '.line_spacing_pt')"
                />
              </div>
              <div class="gd-field">
                <label class="gd-label">首行缩进 (字)</label>
                <input
                  class="gd-input"
                  type="number"
                  step="0.5"
                  min="0"
                  :disabled="readOnly"
                  :value="draftValue(row.role + '.first_line_indent_chars', row.style.first_line_indent_chars)"
                  @input="onDraft(row.role + '.first_line_indent_chars', ($event.target as HTMLInputElement).value)"
                  @blur="commitDraft((v) => withStyle(local, row.role, { first_line_indent_chars: v } as Partial<ParaStyle>), row.role + '.first_line_indent_chars')"
                />
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- 字体候选，供上面的 datalist 使用 -->
    <datalist id="gd-font-list">
      <option v-for="f in FONT_CHOICES" :key="f" :value="f" />
    </datalist>
  </div>
</template>