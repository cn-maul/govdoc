/**
 * 规则编辑器的纯函数测试。
 *
 * 这些函数决定「保存一份规则时会不会丢字段、会不会把不相干 role 一起改掉」。
 * 那类问题在界面上看不出来（点几下看起来都正常），却在保存后才暴露成
 * 「缺某个元素的样式」或「明明只改了标题，正文也变了」，所以必须在这里锁住。
 *
 * 用 node:test + node:assert，无第三方测试框架依赖。
 */

import { strict as assert } from 'node:assert'
import { test } from 'node:test'
import {
  EDITOR_ROLE_ORDER,
  EDITOR_ROLE_PRIMARY,
  FONT_SIZE_ORDER,
  FONT_SIZE_POINTS,
  isKnownFontSize,
  profileName,
  specSummary,
  trimNum,
  type Profile,
  type Role,
  type Spec,
} from '../src/api/types.ts'
import {
  alignLabel,
  canDelete,
  cloneSpec,
  fromNumberText,
  isBuiltin,
  sizeLabel,
  suggestCopyName,
  toNumberText,
  validateSpec,
  withPage,
  withStyle,
} from '../src/lib/specEdit.ts'

/** 造一份最小可用的规则表：每个 role 都有样式，避免 validateSpec 全是「缺样式」。 */
function makeSpec(): Spec {
  const styles = {} as Record<Role, Spec['styles'][Role]>
  for (const role of EDITOR_ROLE_ORDER) {
    styles[role] = {
      role,
      name: role,
      font: '仿宋_GB2312',
      latin_font: 'Times New Roman',
      size: '三号',
      bold: false,
      color: '000000',
      align: 'left',
      line_rule: 'exact',
      line_spacing_pt: 28.99,
      space_before_lines: 0,
      space_after_lines: 0,
      first_line_indent_chars: 0,
      left_indent_chars: 0,
      right_indent_chars: 0,
      hanging_indent_chars: 0,
      keep_with_next: false,
    }
  }
  return {
    page: {
      width_mm: 210,
      height_mm: 297,
      margin_top_mm: 37,
      margin_bottom_mm: 35,
      margin_left_mm: 28,
      margin_right_mm: 26,
      lines_per_page: 22,
      chars_per_line: 28,
      header_mm: 20,
      footer_mm: 20,
    },
    direction: '下行文',
    styles,
    page_number_size: '四号',
    page_number_font: '宋体',
    page_number_dash: '—',
  }
}

// ---------------------------------------------------------------- cloneSpec

test('cloneSpec 之后改副本不影响原表', () => {
  const a = makeSpec()
  const b = cloneSpec(a)
  b.styles.body.font = '宋体'
  b.page.margin_top_mm = 1
  assert.equal(a.styles.body.font, '仿宋_GB2312', '原表不应被改')
  assert.equal(a.page.margin_top_mm, 37, '原页面不应被改')
})

test('cloneSpec 保留编辑器未列出的 role', () => {
  const a = makeSpec()
  // 模拟服务端返回了 EDITOR_ROLE_ORDER 之外的 role。
  ;(a.styles as Record<string, unknown>).future_role = { role: 'future_role' }
  const b = cloneSpec(a)
  assert.ok(
    (b.styles as Record<string, unknown>).future_role,
    '未列出的 role 必须在复制时保留，否则保存时会静默丢掉它',
  )
})

// ---------------------------------------------------------------- withStyle

test('withStyle 只改目标 role', () => {
  const a = makeSpec()
  const b = withStyle(a, 'title', { font: '黑体', size: '二号' })
  assert.equal(b.styles.title.font, '黑体')
  assert.equal(b.styles.title.size, '二号')
  assert.equal(b.styles.body.font, '仿宋_GB2312', '正文不应被连带改掉')
  assert.equal(b.styles.heading1.font, '仿宋_GB2312', '一级标题不应被连带改掉')
})

test('withStyle 不就地修改入参', () => {
  const a = makeSpec()
  withStyle(a, 'body', { font: '宋体' })
  assert.equal(a.styles.body.font, '仿宋_GB2312', '入参被就地改了：界面将无法判断「有无未保存修改」')
})

test('withStyle 作用于未知 role 时不崩溃', () => {
  const a = makeSpec()
  const b = withStyle(a, 'nonexistent' as Role, { font: '宋体' })
  assert.equal(b.styles.body.font, '仿宋_GB2312')
})

test('withPage 只改页面几何', () => {
  const a = makeSpec()
  const b = withPage(a, { margin_left_mm: 30 })
  assert.equal(b.page.margin_left_mm, 30)
  assert.equal(b.page.margin_right_mm, 26, '其它页边距不应被改')
  assert.equal(b.styles.body.font, '仿宋_GB2312')
})

test('withPage 之后原表不受影响', () => {
  const a = makeSpec()
  withPage(a, { width_mm: 999 })
  assert.equal(a.page.width_mm, 210)
})

// ---------------------------------------------------------------- validateSpec

test('完整规则表无校验问题', () => {
  assert.deepEqual(validateSpec(makeSpec()), [])
})

test('空字体被拦下', () => {
  const s = withStyle(makeSpec(), 'body', { font: '  ' })
  const problems = validateSpec(s)
  assert.ok(problems.some((p) => p.role === 'body' && p.message.includes('未设置字体')))
})

test('非标准字号被拦下', () => {
  const s = withStyle(makeSpec(), 'body', { size: '28pt' as never })
  const problems = validateSpec(s)
  assert.ok(problems.some((p) => p.role === 'body' && p.message.includes('不是标准字号')))
})

test('固定行距为 0 被拦下', () => {
  const s = withStyle(makeSpec(), 'body', { line_rule: 'exact', line_spacing_pt: 0 })
  const problems = validateSpec(s)
  assert.ok(
    problems.some((p) => p.role === 'body' && p.message.includes('行距磅值')),
    '固定值 0 磅会把整段压成一条线，必须拦下',
  )
})

test('自动行距不要求磅值', () => {
  const s = withStyle(makeSpec(), 'body', { line_rule: 'auto', line_spacing_pt: 0 })
  assert.deepEqual(validateSpec(s), [], 'auto 模式下 0 是合法的')
})

test('负缩进被拦下', () => {
  const s = withStyle(makeSpec(), 'body', { first_line_indent_chars: -1 })
  assert.ok(validateSpec(s).some((p) => p.message.includes('首行缩进')))
})

test('页边距大于页宽被拦下', () => {
  const s = withPage(makeSpec(), { width_mm: 100, margin_left_mm: 60, margin_right_mm: 60 })
  assert.ok(validateSpec(s).some((p) => p.message.includes('版心没有宽度')))
})

test('页边距大于页高被拦下', () => {
  const s = withPage(makeSpec(), { height_mm: 100, margin_top_mm: 60, margin_bottom_mm: 60 })
  assert.ok(validateSpec(s).some((p) => p.message.includes('版心没有高度')))
})

test('校验只覆盖规则表里存在的 role', () => {
  // 规则表不完整时不应凭空报出「缺样式」，那是服务端的事。
  const s = makeSpec()
  delete (s.styles as Partial<Record<Role, unknown>>).cc
  const problems = validateSpec(s)
  assert.ok(!problems.some((p) => p.role === 'cc'), '缺失 role 由服务端报错，前端不重复')
})

// ---------------------------------------------------------------- 数字适配

test('fromNumberText 解析正常数字', () => {
  assert.equal(fromNumberText('16'), 16)
  assert.equal(fromNumberText('28.99'), 28.99)
  assert.equal(fromNumberText(' 30 '), 30)
})

test('fromNumberText 容忍全角逗号', () => {
  assert.equal(fromNumberText('28，99'), 28.99)
})

test('fromNumberText 空串与非法输入返回 undefined 而不是 NaN', () => {
  // NaN 会被 JSON 序列化成 null，服务端收到 null 会报一个用户看不懂的错。
  assert.equal(fromNumberText(''), undefined)
  assert.equal(fromNumberText('abc'), undefined)
})

test('toNumberText 用 trimNum 的两位小数（展示用）', () => {
  // toNumberText 走 trimNum（两位小数），是给人看的字符串；
  // 编辑器实际提交用 draftValue（保留三位）。两者精度不同是刻意的，不该统一。
  assert.equal(toNumberText(28.990694345), '28.99')
  assert.equal(toNumberText(0), '0')
  assert.equal(toNumberText(NaN), '')
})

// ---------------------------------------------------------------- 内置规则

test('built_in 缺省（omitempty）也算内置判断为 false', () => {
  // Go 侧 built_in=false 时字段根本不存在，不能用 === false 判断。
  const p = { built_in: undefined } as Profile
  assert.equal(isBuiltin(p), false)
})

test('built_in 为 true 时识别为内置', () => {
  assert.equal(isBuiltin({ built_in: true } as Profile), true)
})

test('内置规则不可删除，且给出理由', () => {
  const g = canDelete({ id: 'govdoc', built_in: true } as Profile, 'generic')
  assert.equal(g.ok, false)
  assert.ok(g.why.includes('内置'), '必须说明为什么')
  assert.ok(g.why.includes('复制'), '必须给出替代做法')
})

test('默认规则不可删除，且给出理由', () => {
  const g = canDelete({ id: 'mine', built_in: false } as Profile, 'mine')
  assert.equal(g.ok, false)
  assert.ok(g.why.includes('默认'))
})

test('非内置非默认的规则可删除', () => {
  const g = canDelete({ id: 'mine', built_in: false } as Profile, 'govdoc')
  assert.equal(g.ok, true)
  assert.equal(g.why, '')
})

test('建议的副本名带「副本」', () => {
  assert.equal(suggestCopyName('公文'), '公文 副本')
})

// ---------------------------------------------------------------- 摘要

test('specSummary 给出正文与页边距', () => {
  const s = specSummary(makeSpec())
  assert.ok(s.includes('仿宋_GB2312'))
  assert.ok(s.includes('三号'))
  assert.ok(s.includes('28.99pt'))
  assert.ok(s.includes('37/35/28/26mm'), `页边距摘要缺失: ${s}`)
})

test('specSummary 对缺失规则给出可读文案', () => {
  assert.equal(specSummary(null), '规则无法读取')
  assert.equal(specSummary(undefined), '规则无法读取')
})

test('styleSummary 把一行说成人话', () => {
  const s = specSummary({
    ...makeSpec(),
    styles: {
      ...makeSpec().styles,
      body: { ...makeSpec().styles.body, size: '四号', line_spacing_pt: 20, first_line_indent_chars: 2 },
    },
  })
  assert.ok(s.includes('四号'))
  assert.ok(s.includes('20pt'))
  assert.ok(s.includes('2 字'))
})

test('trimNum 去掉无意义小数', () => {
  assert.equal(trimNum(28.990694345025055), '28.99')
  assert.equal(trimNum(16), '16')
  assert.equal(trimNum(NaN), '—')
})

test('alignLabel 与 sizeLabel', () => {
  assert.equal(alignLabel('both'), '两端对齐')
  assert.equal(alignLabel('center'), '居中')
  assert.equal(alignLabel('left'), '左对齐')
  assert.equal(sizeLabel('三号'), '三号（16pt）')
})

test('profileName 找不到时回显 id 而不是空白', () => {
  const ps = [{ id: 'govdoc', name: '公文', spec: makeSpec() }]
  assert.equal(profileName(ps, 'govdoc'), '公文')
  assert.equal(profileName(ps, 'missing'), 'missing')
  assert.equal(profileName(ps, ''), '')
})

// ---------------------------------------------------------------- 字号表

test('字号表与后端 fontPoints 一致', () => {
  assert.equal(FONT_SIZE_POINTS.初号, 42)
  assert.equal(FONT_SIZE_POINTS.三号, 16)
  assert.equal(FONT_SIZE_POINTS.五号, 10.5)
  assert.equal(FONT_SIZE_POINTS.小五, 9)
})

test('字号下拉覆盖全部标准字号且无重复', () => {
  assert.equal(FONT_SIZE_ORDER.length, 12)
  assert.equal(new Set(FONT_SIZE_ORDER).size, 12)
  for (const s of FONT_SIZE_ORDER) {
    assert.ok(isKnownFontSize(s), `${s} 应是标准字号`)
  }
})

test('isKnownFontSize 拒绝非标准字号', () => {
  assert.equal(isKnownFontSize('28pt'), false)
  assert.equal(isKnownFontSize(''), false)
  // 必须是 own-property 判断：'toString' 之类继承属性会被误判成字号。
  assert.equal(isKnownFontSize('toString'), false)
})

test('默认展开的是标题正文与前三级标题', () => {
  assert.deepEqual(EDITOR_ROLE_PRIMARY, ['title', 'body', 'heading1', 'heading2', 'heading3'])
})