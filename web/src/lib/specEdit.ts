/**
 * 规则编辑器的纯函数层。
 *
 * 单独成文件是为了能被复用与测试：编辑器的正确性（「保存时会不会漏字段」「改
 * 字体时会不会连带改到别的 role」）不依赖任何界面，也不该靠点几下界面来验证。
 */

import {
  EDITOR_ROLE_ORDER,
  FONT_SIZE_POINTS,
  isKnownFontSize,
  ROLE_LABEL,
  trimNum,
  type Align,
  type FontSize,
  type LineRule,
  type ParaStyle,
  type Profile,
  type Role,
  type Spec,
} from '../api/types.ts'

/** 深拷贝一份规则表，避免编辑时直接改到服务端返回的对象。 */
export function cloneSpec(spec: Spec): Spec {
  const styles = {} as Record<Role, ParaStyle>
  for (const role of EDITOR_ROLE_ORDER) {
    const s = spec.styles?.[role]
    if (s) styles[role] = { ...s }
  }
  // 规则表里可能存在编辑器没列出的 role，一并带过去，保存时才不会把它们丢掉。
  for (const role of Object.keys(spec.styles ?? {}) as Role[]) {
    if (!styles[role]) styles[role] = { ...spec.styles[role] }
  }
  return {
    ...spec,
    page: { ...spec.page },
    styles,
  }
}

/**
 * 改成某条样式的某个字段。
 *
 * 返回**新对象**而不是就地改：编辑器每次输入都产生一份新规则，界面靠引用
 * 变化来重渲；就地改会让「是否有未保存修改」判断不出来，保存按钮永远灰着。
 */
export function withStyle(
  spec: Spec,
  role: Role,
  patch: Partial<ParaStyle>,
): Spec {
  const next = cloneSpec(spec)
  const cur = next.styles[role]
  if (!cur) return next
  next.styles[role] = { ...cur, ...patch }
  return next
}

/** 改页面几何（尺寸或四边页边距）。 */
export function withPage(spec: Spec, patch: Partial<Spec['page']>): Spec {
  const next = cloneSpec(spec)
  next.page = { ...next.page, ...patch }
  return next
}

/** 一条校验结果：role 为空表示页面级问题。 */
export interface SpecProblem {
  role: Role | ''
  message: string
}

/**
 * 保存前的本地校验。
 *
 * 只拦**明显会让成品变得没道理**的问题，不做重复劳动：
 *  - 字号必须是标准字号：后端没有字号到磅值的映射，非法字号会渲染成不可预期的大小；
 *  - 字体不能为空：渲染器会回退到某个任意字体，用户看到的与设置的不一致；
 *  - `exact` 行距必须为正：否则「固定值 0 磅」会把整段压成看不见的一条线。
 *
 * 其余（缺 role、网格不一致等）交给服务端 —— 它有完整规则，重复实现一遍只会
 * 出现两套不一致的说法。
 */
export function validateSpec(spec: Spec): SpecProblem[] {
  const problems: SpecProblem[] = []

  const p = spec.page
  if (!(p.width_mm > 0) || !(p.height_mm > 0)) {
    problems.push({ role: '', message: '页面宽高必须大于 0。' })
  }
  const margins = [
    ['上', p.margin_top_mm],
    ['下', p.margin_bottom_mm],
    ['左', p.margin_left_mm],
    ['右', p.margin_right_mm],
  ] as const
  for (const [label, v] of margins) {
    if (!(v >= 0)) problems.push({ role: '', message: `页边距「${label}」不能为负。` })
  }
  // 版心必须还剩下空间，否则一行都放不下。
  if (p.width_mm > 0 && p.height_mm > 0) {
    const inner = p.width_mm - p.margin_left_mm - p.margin_right_mm
    const innerH = p.height_mm - p.margin_top_mm - p.margin_bottom_mm
    if (inner <= 0) problems.push({ role: '', message: '左右页边距之和已不小于页宽，版心没有宽度了。' })
    if (innerH <= 0) problems.push({ role: '', message: '上下页边距之和已不小于页高，版心没有高度了。' })
  }

  for (const role of EDITOR_ROLE_ORDER) {
    const s = spec.styles?.[role]
    if (!s) continue
    const name = ROLE_LABEL[role] ?? role
    if (s.font.trim() === '') {
      problems.push({ role, message: `「${name}」未设置字体。` })
    }
    if (!isKnownFontSize(s.size)) {
      problems.push({
        role,
        message: `「${name}」的字号「${s.size}」不是标准字号。`,
      })
    }
    if (s.line_rule === 'exact' && !(s.line_spacing_pt > 0)) {
      problems.push({ role, message: `「${name}」行距设为固定值时，行距磅值必须大于 0。` })
    }
    if (s.first_line_indent_chars < 0) {
      problems.push({ role, message: `「${name}」的首行缩进不能为负。` })
    }
  }

  return problems
}

/**
 * 本地校验通过后仍需服务端校验，因此这里只说明「可以提交」而非「一定成功」。
 *
 * `exact` 行距在编辑器里由用户手填磅值，位数有限；复制内置配置时保留原值
 * 不动，所以正常路径下不会被改动。
 */
export function canSubmit(spec: Spec): boolean {
  return validateSpec(spec).length === 0
}

/** 一行摘要：把某条样式说成人话，用于列表与折叠标题。 */
export function styleSummary(s: ParaStyle | undefined): string {
  if (!s) return '无样式'
  const bits: string[] = [s.font, `${s.size}（${FONT_SIZE_POINTS[s.size] ?? '?'}pt）`]
  if (s.line_spacing_pt > 0) {
    bits.push(`行距 ${trimNum(s.line_spacing_pt)}pt`)
  }
  if (s.first_line_indent_chars > 0) {
    bits.push(`缩进 ${trimNum(s.first_line_indent_chars)} 字`)
  }
  if (s.align && s.align !== 'left') bits.push(alignLabel(s.align))
  if (s.bold) bits.push('加粗')
  return bits.join(' · ')
}

/** 对齐方式的中文名。 */
export function alignLabel(a: Align): string {
  switch (a) {
    case 'center':
      return '居中'
    case 'right':
      return '右对齐'
    case 'both':
      return '两端对齐'
    default:
      return '左对齐'
  }
}

/** 页面几何的一行摘要。 */
export function pageSummary(spec: Spec): string {
  const p = spec.page
  return (
    `${trimNum(p.width_mm)}×${trimNum(p.height_mm)}mm · ` +
    `页边距 ${trimNum(p.margin_top_mm)}/${trimNum(p.margin_bottom_mm)}/${trimNum(p.margin_left_mm)}/${trimNum(p.margin_right_mm)}mm`
  )
}

/**
 * 内置配置能否编辑。
 *
 * 内置规则不可直接改、不可删：它是所有文档的兜底规则，也是恢复到标准样式的
 * 唯一依据。想调整就先「复制一份」，那条路径完全等价，只是留下一份原件。
 */
export function isBuiltin(p: Profile): boolean {
  // Go 侧 omitempty：内置=false 时字段根本不存在，所以用真值判断。
  return !!p.built_in
}

/** 内置配置能否删除 —— 与 canEdit 同因，用于决定删除按钮的禁用与提示。 */
export function canDelete(p: Profile, activeId: string): { ok: boolean; why: string } {
  if (isBuiltin(p)) {
    return {
      ok: false,
      why: '内置配置不可删除：它是所有文档的兜底规则，也是恢复标准样式的依据。想改请先「复制一份」。',
    }
  }
  if (p.id === activeId) {
    return {
      ok: false,
      why: '这是新文档的默认配置，请先在列表里改选其它配置再删除。',
    }
  }
  return { ok: true, why: '' }
}

/** 行距规则的选项。 */
export const LINE_RULES: { value: LineRule; label: string; hint: string }[] = [
  { value: 'exact', label: '固定值', hint: '行高固定为指定磅值，每页行数因此是确定的。' },
  { value: 'auto', label: '自动', hint: '随字号变化，一般排版常用的单倍/多倍行距。' },
]

/** 对齐方式的选项。 */
export const ALIGNS: { value: Align; label: string }[] = [
  { value: 'left', label: '左对齐' },
  { value: 'center', label: '居中' },
  { value: 'right', label: '右对齐' },
  { value: 'both', label: '两端对齐' },
]

/**
 * 数字输入框的字符串 ↔ 数值。
 *
 * 用户在输入框里打的是字符串（可能空、可能有半角逗号）。直接在 v-model 上
 * 绑 number 会让「删掉重新输入」的过程立刻变成 NaN 并把字段清零，所以中间
 * 必须留一层字符串，由失焦或保存时才转数值。
 */
export function toNumberText(v: number | undefined): string {
  if (v === undefined || v === null) return ''
  return Number.isFinite(v) ? trimNum(v) : ''
}

/**
 * 把输入框字符串转成数值；空串或非法输入返回 undefined 而不是 NaN。
 *
 * 中文输入法下小数点很可能打成全角「，」。直接 Number() 会得到 NaN，而把逗号
 * 一删了事又会得到 2899 —— 一个看起来正常、其实大 100 倍的行距，页面会排成
 * 莫名其妙的形状。所以全角逗号必须替换成半角小数点，而不是删掉。
 */
export function fromNumberText(s: string): number | undefined {
  const t = s.trim().replace(/，/g, '.')
  if (t === '') return undefined
  const v = Number(t)
  return Number.isFinite(v) ? v : undefined
}

/** 建议的新建配置名：`<原名> 副本`，避免用户凭空想名字。 */
export function suggestCopyName(name: string): string {
  return `${name} 副本`
}

/** 供界面显示的字号全称，如「三号（16pt）」。 */
export function sizeLabel(size: FontSize): string {
  return `${size}（${FONT_SIZE_POINTS[size] ?? '?'}pt）`
}