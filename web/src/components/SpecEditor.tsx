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
import { useEffect, useMemo, useState } from 'react'
import { Button } from '@heroui/react'
import {
  EDITOR_ROLE_PRIMARY,
  FONT_CHOICES,
  FONT_SIZE_ORDER,
  FONT_SIZE_POINTS,
  BINDABLE_ROLES,
  defaultOutline,
  ROLE_LABEL,
  outlineOf,
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
  styleSummary,
  validateSpec,
  withHeadingForm,
  withOutline,
  withPage,
  withStyle,
} from '../lib/specEdit'
import { useGov } from '../store/useGov'
import {
  AlertBox,
  CheckboxField,
  Field,
  Select,
  TextArea,
  TextInput,
  type SelectItem,
} from '../ui/primitives'

/**
 * 字号选项。
 *
 * 标准字号之外还留一条「（非标准）」：服务端允许历史配置带着非标准字号存在
 * （读得出来、也渲染得出来），编辑器不能假装它不存在——否则下拉框会显示成
 * 空值，用户一碰就把别人的配置改成了三号。这一项只在当前值非标准时出现。
 */
function sizeItems(current: string): SelectItem[] {
  const items: SelectItem[] = FONT_SIZE_ORDER.map((s) => ({
    value: s,
    label: `${s}（${FONT_SIZE_POINTS[s]}pt）`,
  }))
  if (!FONT_SIZE_ORDER.includes(current as FontSize)) {
    items.push({ value: current, label: `${current}（非标准）` })
  }
  return items
}

export interface SpecEditorProps {
  spec: Spec
  /** 内置配置：只读，且给出为什么不能改。 */
  readOnly?: boolean
  onChange: (spec: Spec) => void
}

interface StyleRow {
  role: Role
  label: string
  style: ParaStyle | undefined
}

type PageNumberKey =
  | 'width_mm'
  | 'height_mm'
  | 'margin_top_mm'
  | 'margin_bottom_mm'
  | 'margin_left_mm'
  | 'margin_right_mm'

const PAGE_FIELDS: { path: string; label: string; key: PageNumberKey }[] = [
  { path: 'page.width_mm', label: '页宽 (mm)', key: 'width_mm' },
  { path: 'page.height_mm', label: '页高 (mm)', key: 'height_mm' },
  { path: 'page.margin_top_mm', label: '上边距 (mm)', key: 'margin_top_mm' },
  { path: 'page.margin_bottom_mm', label: '下边距 (mm)', key: 'margin_bottom_mm' },
  { path: 'page.margin_left_mm', label: '左边距 (mm)', key: 'margin_left_mm' },
  { path: 'page.margin_right_mm', label: '右边距 (mm)', key: 'margin_right_mm' },
]

export default function SpecEditor({ spec, readOnly = false, onChange }: SpecEditorProps) {
  useGov()

  /** 本地副本：不在 props 上就地改。 */
  const [local, setLocal] = useState<Spec>(() => cloneSpec(spec))

  // 父组件换了一份规则（比如切换编辑对象）就重新拷贝。
  useEffect(() => {
    setLocal(cloneSpec(spec))
  }, [spec])

  function commit(next: Spec): void {
    if (readOnly) return
    setLocal(next)
    onChange(next)
  }

  // ---------------------------------------------------------------- 展开状态

  /** 默认展开标题、正文与前三级标题：用户调版式时真正会动的就是这几项。 */
  const [open, setOpen] = useState<Set<Role>>(() => new Set(EDITOR_ROLE_PRIMARY))
  const [showAdvanced, setShowAdvanced] = useState(false)

  function toggle(role: Role): void {
    setOpen((prev) => {
      const s = new Set(prev)
      if (s.has(role)) s.delete(role)
      else s.add(role)
      return s
    })
  }

  const problems = useMemo(() => validateSpec(local), [local])
  const outline = useMemo(() => outlineOf(local), [local])
  const problemsByRole = useMemo(() => {
    const m = new Map<string, string[]>()
    for (const p of problems) {
      const k = p.role
      const arr = m.get(k) ?? []
      arr.push(p.message)
      m.set(k, arr)
    }
    return m
  }, [problems])

  // ---------------------------------------------------------------- 输入适配

  /**
   * 数字输入框用字符串承载，失焦时转回数值写进 spec。
   *
   * 用户删掉重打的过程中，值会短暂地是空串；此时不能把它当成 0 立刻提交，
   * 否则用户清空后重新输入的数字会被前面的 0 盖掉，界面上还会闪一下 0。
   * 所以草稿按「字段路径」存在 drafts 里，键路径必须与 onXxxBlur 读的一致。
   */
  const [drafts, setDrafts] = useState<Record<string, string>>({})

  /** 读取草稿值：没有草稿时显示当前值。 */
  function draftValue(path: string, value: number): string {
    const d = drafts[path]
    if (d !== undefined) return d
    return Number.isFinite(value) ? String(Math.round(value * 1000) / 1000) : ''
  }

  /** 记录草稿值。 */
  function onDraft(path: string, raw: string): void {
    setDrafts((prev) => ({ ...prev, [path]: raw }))
  }

  /** 失焦时把草稿转成数值提交；空串按 0 处理，因为这些字段 0 是合法值。 */
  function commitDraft(patch: (v: number) => Spec, path: string): void {
    const raw = drafts[path]
    if (raw === undefined) return
    const v = fromNumberText(raw)
    commit(patch(v ?? 0))
    setDrafts((prev) => {
      if (!(path in prev)) return prev
      const next = { ...prev }
      delete next[path]
      return next
    })
  }

  // ---------------------------------------------------------------- role 列表

  const primaryRows = useMemo<StyleRow[]>(
    () =>
      EDITOR_ROLE_PRIMARY.map((r) => ({
        role: r,
        label: ROLE_LABEL[r] ?? r,
        style: local.styles?.[r],
      })),
    [local],
  )

  const otherRows = useMemo<StyleRow[]>(() => {
    const keys = Object.keys(local.styles ?? {}) as Role[]
    return keys
      .filter((r) => !EDITOR_ROLE_PRIMARY.includes(r))
      .map((r) => ({ role: r, label: ROLE_LABEL[r] ?? r, style: local.styles?.[r] }))
      .sort((a, b) => a.label.localeCompare(b.label, 'zh-Hans-CN'))
  }, [local])

  return (
    <div className="flex flex-col gap-4">
      {readOnly && (
        <AlertBox status="info" title="这是内置配置，不能直接修改" />
      )}

      {problems.length > 0 && (
        <AlertBox status="warning" title={`规则还有 ${problems.length} 处需要修正`}>
          <div className="flex flex-col gap-0.5 text-xs">
            {problems.map((p, i) => (
              <div key={i}>· {p.message}</div>
            ))}
          </div>
        </AlertBox>
      )}

      {/* 页面 */}
      <section className="rounded-lg border border-border px-4 py-3.5">
        <h4 className="mb-1 text-sm font-semibold">页面与页边距</h4>

        <div className="mt-3 grid grid-cols-[repeat(auto-fit,minmax(120px,1fr))] gap-3">
          {PAGE_FIELDS.map((f) => (
            <Field key={f.path} label={f.label}>
              <TextInput
                type="number"
                step="0.1"
                disabled={readOnly}
                value={draftValue(f.path, local.page[f.key])}
                onChange={(e) => onDraft(f.path, e.target.value)}
                onBlur={() =>
                  commitDraft(
                    (v) => withPage(local, { [f.key]: v } as Partial<Spec['page']>),
                    f.path,
                  )
                }
              />
            </Field>
          ))}
        </div>
      </section>

      {/* 体裁结构 */}
      <section className="rounded-lg border border-border px-4 py-3.5">
        <h4 className="mb-1 text-sm font-semibold">体裁结构</h4>
        <p className="text-xs leading-relaxed text-muted">
          这里决定这种文稿「应该长成什么结构」：要有哪些要素、章节怎么编号、最多分几级。
          换一份配置就换一种文体，模型会按这份规则整理文档。
        </p>

        <div className="mt-3 flex flex-col gap-4">
          <Field
            label="必需要素"
            hint="勾选这种文稿应当具备的要素。缺少时只提示，不阻止导出 —— 很多文稿本来就没有抄送或份号。"
          >
            <div className="grid grid-cols-[repeat(auto-fit,minmax(120px,1fr))] gap-x-3 gap-y-2">
              {BINDABLE_ROLES.map((role) => (
                <CheckboxField
                  key={role}
                  isDisabled={readOnly}
                  isSelected={(outline.required_elements ?? []).includes(role)}
                  onChange={(checked) => {
                    const cur = outline.required_elements ?? []
                    const next = checked
                      ? [...cur, role]
                      : cur.filter((r) => r !== role)
                    commit(withOutline(local, { required_elements: next }))
                  }}
                  label={ROLE_LABEL[role] ?? role}
                />
              ))}
            </div>
          </Field>

          <div className="grid grid-cols-[repeat(auto-fit,minmax(200px,1fr))] gap-3">
            <Field label="最多几级标题">
              <Select
                aria-label="最多几级标题"
                isDisabled={readOnly}
                value={String(outline.max_depth)}
                onChange={(v) => {
                  const depth = Number(v)
                  const defaults = defaultOutline().heading_forms ?? []
                  const forms = [...(outline.heading_forms ?? [])]
                  while (forms.length < depth) {
                    forms.push({ ...(defaults[forms.length] ?? { numeral: 'arabic', suffix: '.' }) })
                  }
                  commit(
                    withOutline(local, {
                      max_depth: depth,
                      // 每一级都必须有编号形式；减少层级时多余的保留，方便改回去。
                      heading_forms: forms,
                    }),
                  )
                }}
                items={[1, 2, 3, 4].map((n) => ({ value: String(n), label: `${n} 级` }))}
              />
            </Field>
            <Field label="主送机关">
              <CheckboxField
                isDisabled={readOnly}
                isSelected={outline.requires_recipient}
                onChange={(v) =>
                  commit(withOutline(local, { requires_recipient: v }))
                }
                label="必须有主送机关"
              />
            </Field>
            <Field label="正文前言">
              <CheckboxField
                isDisabled={readOnly}
                isSelected={outline.preamble}
                onChange={(v) => commit(withOutline(local, { preamble: v }))}
                label="主送机关下有一段不编号的引言"
              />
            </Field>
          </div>

          <div className="space-y-2">
            <div className="text-sm font-medium">标题编号形式</div>
            {(outline.heading_forms ?? []).slice(0, outline.max_depth).map((form, i) => (
              <div
                key={i}
                className="grid grid-cols-[repeat(auto-fit,minmax(110px,1fr))] items-end gap-2"
              >
                <Field label={`第 ${i + 1} 级`}>
                  <Select
                    aria-label={`第 ${i + 1} 级编号数字`}
                    isDisabled={readOnly}
                    value={form.numeral}
                    onChange={(v) =>
                      commit(
                        withHeadingForm(local, i, {
                          numeral: v === 'arabic' ? 'arabic' : 'chinese',
                        }),
                      )
                    }
                    items={[
                      { value: 'chinese', label: '中文数字（一、二）' },
                      { value: 'arabic', label: '阿拉伯数字（1、2）' },
                    ]}
                  />
                </Field>
                <Field label="数字前">
                  <TextInput
                    type="text"
                    disabled={readOnly}
                    placeholder="例如 （"
                    value={form.prefix ?? ''}
                    onChange={(e) =>
                      commit(withHeadingForm(local, i, { prefix: e.target.value }))
                    }
                  />
                </Field>
                <Field label="数字后">
                  <TextInput
                    type="text"
                    disabled={readOnly}
                    placeholder="例如 、"
                    value={form.suffix ?? ''}
                    onChange={(e) =>
                      commit(withHeadingForm(local, i, { suffix: e.target.value }))
                    }
                  />
                </Field>
                <div className="pb-2 text-sm text-muted">
                  示例：{form.prefix ?? ''}
                  {form.numeral === 'arabic' ? '1' : '一'}
                  {form.suffix ?? ''}
                </div>
              </div>
            ))}
          </div>

          <Field
            label="语言风格要求"
            hint="会原样写进模型提示词。例如「庄重、简明，不使用第一人称」。"
          >
            <TextArea
              rows={2}
              disabled={readOnly}
              value={outline.tone ?? ''}
              onChange={(e) => commit(withOutline(local, { tone: e.target.value }))}
            />
          </Field>
          <Field
            label="附加要求"
            hint="这类文稿的其它规矩，例如固定结尾用语、必须附上的内容。"
          >
            <TextArea
              rows={3}
              disabled={readOnly}
              value={outline.guidance ?? ''}
              onChange={(e) => commit(withOutline(local, { guidance: e.target.value }))}
            />
          </Field>
        </div>
      </section>

      {/* 常用元素 */}
      <section className="rounded-lg border border-border px-4 py-3.5">
        <h4 className="mb-1 text-sm font-semibold">主要元素</h4>

        {primaryRows.map((row, index) => (
          <div
            key={row.role}
            className={index === 0 ? 'mt-2' : 'mt-2 border-t border-border pt-2'}
          >
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-expanded={open.has(row.role)}
              isDisabled={readOnly || !row.style}
              onPress={() => toggle(row.role)}
              className="h-auto w-full justify-start gap-2 px-1 py-1.5 text-left"
            >
              <span
                className={
                  open.has(row.role)
                    ? 'inline-block rotate-90 text-xs text-muted transition-transform'
                    : 'inline-block text-xs text-muted transition-transform'
                }
                data-open={open.has(row.role)}
                aria-hidden="true"
              >
                ▸
              </span>
              <span className="whitespace-nowrap font-semibold">{row.label}</span>
              <span className="truncate text-[13px] text-muted">{styleSummary(row.style)}</span>
            </Button>

            {open.has(row.role) && row.style && (
              <div className="pb-1 pl-5 pr-1 pt-2.5">
                {(problemsByRole.get(row.role) ?? []).length > 0 && (
                  <div className="mb-3 flex flex-col gap-0.5 rounded-xl bg-warning-soft px-3 py-2 text-xs leading-snug text-warning-soft-foreground">
                    {(problemsByRole.get(row.role) ?? []).map((m, i) => (
                      <div key={i}>· {m}</div>
                    ))}
                  </div>
                )}

                <div className="grid grid-cols-[repeat(auto-fit,minmax(120px,1fr))] gap-3">
                  <Field label="字体">
                    <TextInput
                      list="spec-font-list"
                      type="text"
                      disabled={readOnly}
                      value={row.style.font}
                      onChange={(e) =>
                        commit(withStyle(local, row.role, { font: e.target.value }))
                      }
                    />
                  </Field>
                  <Field label="字号">
                    <Select
                      aria-label="字号"
                      isDisabled={readOnly}
                      value={row.style.size}
                      onChange={(v) =>
                        commit(withStyle(local, row.role, { size: v as FontSize }))
                      }
                      items={sizeItems(row.style.size)}
                    />
                  </Field>
                  <Field label="行距">
                    <Select
                      aria-label="行距"
                      isDisabled={readOnly}
                      value={row.style.line_rule}
                      onChange={(v) =>
                        commit(withStyle(local, row.role, { line_rule: v as LineRule }))
                      }
                      items={LINE_RULES.map((r) => ({
                        value: r.value,
                        label: r.label,
                        hint: r.hint,
                      }))}
                    />
                  </Field>
                  <Field label="行距 (pt)">
                    <TextInput
                      type="number"
                      step="0.1"
                      min="0"
                      disabled={readOnly || row.style.line_rule !== 'exact'}
                      value={draftValue(
                        row.role + '.line_spacing_pt',
                        row.style.line_spacing_pt,
                      )}
                      onChange={(e) =>
                        onDraft(row.role + '.line_spacing_pt', e.target.value)
                      }
                      onBlur={() =>
                        commitDraft(
                          (v) =>
                            withStyle(local, row.role, {
                              line_spacing_pt: v,
                            } as Partial<ParaStyle>),
                          row.role + '.line_spacing_pt',
                        )
                      }
                    />
                  </Field>
                  <Field label="首行缩进 (字)">
                    <TextInput
                      type="number"
                      step="0.5"
                      min="0"
                      disabled={readOnly}
                      value={draftValue(
                        row.role + '.first_line_indent_chars',
                        row.style.first_line_indent_chars,
                      )}
                      onChange={(e) =>
                        onDraft(row.role + '.first_line_indent_chars', e.target.value)
                      }
                      onBlur={() =>
                        commitDraft(
                          (v) =>
                            withStyle(local, row.role, {
                              first_line_indent_chars: v,
                            } as Partial<ParaStyle>),
                          row.role + '.first_line_indent_chars',
                        )
                      }
                    />
                  </Field>
                  <Field label="对齐">
                    <Select
                      aria-label="对齐"
                      isDisabled={readOnly}
                      value={row.style.align}
                      onChange={(v) => commit(withStyle(local, row.role, { align: v as Align }))}
                      items={ALIGNS}
                    />
                  </Field>
                </div>
              </div>
            )}
          </div>
        ))}
      </section>

      {/* 其余元素 */}
      <section className="rounded-lg border border-border px-4 py-3.5">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onPress={() => setShowAdvanced((prev) => !prev)}
          className="h-auto w-full justify-start gap-2 rounded-none border-b border-border px-1 py-1.5 text-left"
        >
          <span
            className={
              showAdvanced
                ? 'inline-block rotate-90 text-xs text-muted transition-transform'
                : 'inline-block text-xs text-muted transition-transform'
            }
            data-open={showAdvanced}
            aria-hidden="true"
          >
            ▸
          </span>
          <span className="whitespace-nowrap font-semibold">
            其余元素（{otherRows.length} 项）
          </span>
        </Button>

        {showAdvanced && (
          <div className="pb-1 pl-5 pr-1 pt-2.5">
            {otherRows.map((row) => (
              <div
                key={row.role}
                className="mt-2 border-t border-border pt-2 first:mt-0 first:border-t-0 first:pt-0"
              >
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-expanded={open.has(row.role)}
                  isDisabled={readOnly || !row.style}
                  onPress={() => toggle(row.role)}
                  className="h-auto w-full justify-start gap-2 px-1 py-1.5 text-left"
                >
                  <span
                    className={
                      open.has(row.role)
                        ? 'inline-block rotate-90 text-xs text-muted transition-transform'
                        : 'inline-block text-xs text-muted transition-transform'
                    }
                    data-open={open.has(row.role)}
                    aria-hidden="true"
                  >
                    ▸
                  </span>
                  <span className="whitespace-nowrap font-semibold">{row.label}</span>
                  <span className="truncate text-[13px] text-muted">
                    {styleSummary(row.style)}
                  </span>
                </Button>
                {open.has(row.role) && row.style && (
                  <div className="pb-1 pl-5 pr-1 pt-2.5">
                    <div className="grid grid-cols-[repeat(auto-fit,minmax(120px,1fr))] gap-3">
                      <Field label="字体">
                        <TextInput
                          list="spec-font-list"
                          type="text"
                          disabled={readOnly}
                          value={row.style.font}
                          onChange={(e) =>
                            commit(withStyle(local, row.role, { font: e.target.value }))
                          }
                        />
                      </Field>
                      <Field label="字号">
                        <Select
                          aria-label="字号"
                          isDisabled={readOnly}
                          value={row.style.size}
                          onChange={(v) =>
                            commit(withStyle(local, row.role, { size: v as FontSize }))
                          }
                          items={sizeItems(row.style.size)}
                        />
                      </Field>
                      <Field label="行距 (pt)">
                        <TextInput
                          type="number"
                          step="0.1"
                          min="0"
                          disabled={readOnly}
                          value={draftValue(
                            row.role + '.line_spacing_pt',
                            row.style.line_spacing_pt,
                          )}
                          onChange={(e) =>
                            onDraft(row.role + '.line_spacing_pt', e.target.value)
                          }
                          onBlur={() =>
                            commitDraft(
                              (v) =>
                                withStyle(local, row.role, {
                                  line_spacing_pt: v,
                                } as Partial<ParaStyle>),
                              row.role + '.line_spacing_pt',
                            )
                          }
                        />
                      </Field>
                      <Field label="首行缩进 (字)">
                        <TextInput
                          type="number"
                          step="0.5"
                          min="0"
                          disabled={readOnly}
                          value={draftValue(
                            row.role + '.first_line_indent_chars',
                            row.style.first_line_indent_chars,
                          )}
                          onChange={(e) =>
                            onDraft(
                              row.role + '.first_line_indent_chars',
                              e.target.value,
                            )
                          }
                          onBlur={() =>
                            commitDraft(
                              (v) =>
                                withStyle(local, row.role, {
                                  first_line_indent_chars: v,
                                } as Partial<ParaStyle>),
                              row.role + '.first_line_indent_chars',
                            )
                          }
                        />
                      </Field>
                    </div>
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </section>

      {/* 字体候选，供上面的 datalist 使用 */}
      <datalist id="spec-font-list">
        {FONT_CHOICES.map((f) => (
          <option key={f} value={f} />
        ))}
      </datalist>
    </div>
  )
}
