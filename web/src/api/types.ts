/**
 * GovDoc HTTP 线格式类型。
 *
 * 本文件是 internal/api/types.go 与 internal/docmodel/{model,verify}.go 的
 * TypeScript 镜像，字段名与 json tag 一一对应。后端改字段名 = 破坏性变更，
 * 这里必须同步改。
 *
 * 约定：字段名用 snake_case，与 Go 的 json tag 完全一致，不做驼峰转换，
 * 这样浏览器里看到的对象和服务端 JSON 逐字段可比对。
 */

// ---------------------------------------------------------------- docmodel

/** NodeKind 是原文可寻址块的分类。 */
export type NodeKind =
  | 'paragraph'
  | 'heading'
  | 'list_item'
  | 'table'
  | 'image'
  | 'page_break'

/** Severity 是校验诊断的严重级别。 */
export type Severity = 'error' | 'warning' | 'info'

/** Role 是公文元素，决定套用哪一条 GB/T 9704-2012 样式。 */
export type Role =
  | 'copy_number'
  | 'security'
  | 'urgency'
  | 'masthead'
  | 'doc_number'
  | 'signatory'
  | 'red_line'
  | 'title'
  | 'recipient'
  | 'body'
  | 'heading1'
  | 'heading2'
  | 'heading3'
  | 'heading4'
  | 'attachment_note'
  | 'signature'
  | 'date'
  | 'note'
  | 'cc'
  | 'printer'
  | 'attachment'

/** Direction 是行文方向。 */
export type Direction = '下行文' | '上行文' | '平行文'

/** FontSize 是汉字字号名。 */
export type FontSize =
  | '初号'
  | '小初'
  | '一号'
  | '小一'
  | '二号'
  | '小二'
  | '三号'
  | '小三'
  | '四号'
  | '小四'
  | '五号'
  | '小五'

/** Align 是水平对齐（both = 两端对齐）。 */
export type Align = 'left' | 'center' | 'right' | 'both'

/** LineRule 是行距规则（exact = 固定值）。 */
export type LineRule = 'exact' | 'auto'

/** Run 是一段共享行内样式的文本，来自原文。 */
export interface Run {
  text: string
  bold?: boolean
  italic?: boolean
  source_font?: string
}

/** ImageRef 是随管线传递的图片引用（原始字节不经 JSON）。 */
export interface ImageRef {
  name: string
  mime_type: string
}

/** Node 是原文中一个可寻址块。text 必须逐字不变。 */
export interface Node {
  id: string
  kind: NodeKind
  level?: number
  /** VERBATIM 原文文本：任何阶段都不得改写。 */
  text: string
  runs?: Run[]
  rows?: string[][]
  header_row?: boolean
  order: number
  source_style?: string
  source_page?: number
}

/** DocumentModel 是一次上传解析的结果。 */
export interface DocumentModel {
  source_name: string
  format: string
  nodes: Node[]
}

/** Section 是模型提出的章节树节点。它从不携带正文，只引用原文节点 id。 */
export interface Section {
  id: string
  /** 章节标题文本。 */
  title: string
  /** 层次序数深度：1 → 「一、」，2 → 「（一）」，3 → 「1.」，4 → 「（1）」。 */
  level: number
  /** 指向原文标题节点；为空表示标题由模型生成。 */
  heading_node_id?: string
  /** 本节正文引用的原文节点 id，按输出顺序排列。 */
  body_node_ids?: string[]
  /**
   * 本节正文引用的「重组分片」（对应 docmodel.Section.BodyParts）。
   *
   * 一个很长的原文段落被声明重组后，会拆成多个分片；每个分片用
   * `{ node_id, part }` 定位，分别放进不同章节。普通段落仍用 body_node_ids。
   */
  body_parts?: BodyPart[]
  /** 为 false 表示模型有意调整了节内顺序。 */
  preserve_order?: boolean
  children?: Section[]
  /** 模型的简短理由，供人工复核。 */
  rationale?: string
}

/** BodyPart 是重组段落中的一片（对应 docmodel.BodyPart）。 */
export interface BodyPart {
  node_id: string
  /** 分片下标，0 基，对应 plan.recompose 里的 parts 顺序。 */
  part: number
  /** 服务端保留字段；正文分片不应设置 role，角色分片由 Recompose.roles 声明。 */
  role?: string | null
}

/**
 * Recompose 是一段原文被改写成多段的显式声明（对应 docmodel.Recompose）。
 *
 * 用于原文「一段到底」、没有层次结构的文档：模型可以重建章节，
 * 但必须把新段落全文写在这里，供用户逐条校对，并在导出时进入改动日志。
 */
export interface Recompose {
  node_id: string
  /** 改写后的段落，按输出顺序排列。 */
  parts: string[]
  /**
   * 与 parts 等长；空字符串/null 表示普通正文。
   *
   * 仅支持 recipient（主送机关）和 preamble（正文前言）。它们必须位于
   * parts 开头，并且不再放进章节的 body_parts。
   */
  roles?: Array<'recipient' | 'preamble' | '' | null>
  /** 中文理由。 */
  reason?: string
}

/**
 * EditKind 是一处已声明改动的分类（对应 docmodel.EditKind）。
 *
 * 核心不变式放宽为「原文可以被修改，但每一处修改都必须显式声明、可见、可审计」，
 * 于是「改动」本身成了线上类型：模型必须逐条给出 before/after/reason，
 * 校验器逐字比对时才认得出「已声明的修改」与「偷偷改写」的区别。
 */
export type EditKind =
  | 'typo'
  | 'grammar'
  | 'wording'
  | 'punctuation'
  | 'sentence_split'
  | 'heading'
  | 'other'

/**
 * Edit 是一处已声明的改动（对应 docmodel.Edit）。
 *
 * before 必须与原文完全一致（校验要求它出现在该节点文本里），这条约束正是
 * 「改动只能落在真实存在的文字上」的机械保证。
 */
export interface Edit {
  /**
   * 被修改的原文节点 id。**必填**：edit 是在某个节点内部替换文字，没有节点
   * 就无处可改。空 node_id 会被服务端记为「未生效」并在原因里说明正确做法，
   * 而不是被静默丢弃。
   *
   * 给原文补一个段落标题**不走这里**：那是结构问题，用「章节填 title 并留空
   * heading_node_id」表达，生成的标题会计入 report.synthesized_headings 供复核。
   */
  node_id?: string
  kind: EditKind
  /** 必须与原文完全一致的待替换片段。 */
  before: string
  /** 替换后的文字。 */
  after: string
  /** 中文理由，改动日志里给人工复核看。 */
  reason?: string
  /** before 在节点内多次出现时，指定替换第几处（0 基，默认第一处）。 */
  occurrence?: number
}

/** 改动类型的展示顺序与 docmodel.AllEditKinds() 一致。 */
export const EDIT_KINDS: EditKind[] = [
  'typo',
  'grammar',
  'wording',
  'punctuation',
  'sentence_split',
  'heading',
  'other',
]

/** 改动类型的中文标签，与 docmodel.EditLabel() 逐字一致。 */
export const EDIT_KIND_LABEL: Record<EditKind, string> = {
  typo: '错别字',
  grammar: '语病/语句不通',
  wording: '用词不严谨',
  punctuation: '标点',
  sentence_split: '断句',
  heading: '标题',
  other: '其他修改',
}

/** 未收录的 kind 也给出可读标签，避免界面上出现裸英文。 */
export function editLabel(kind: string): string {
  return EDIT_KIND_LABEL[kind as EditKind] ?? kind
}

/** Meta 是文种级元数据，全部可选。 */
export interface Meta {
  copy_number?: string
  security?: string
  urgency?: string
  issuing_org?: string
  doc_number?: string
  signatory?: string
  title?: string
  recipient?: string
  attachment_note?: string
  signature?: string
  date?: string
  note?: string
  cc?: string
  printer?: string
  print_date?: string
}

/** Plan 是模型给出的完整重构方案，也是用户在校对区编辑的对象。 */
export interface Plan {
  meta: Meta
  /**
   * 绑定到公文要素槽位的原文节点（对应 docmodel.Plan.Elements）。
   *
   * 真实上传的公文里「标题」「主送机关」「发文机关署名」「成文日期」「抄送」
   * 本身就是普通段落。不把它们绑定到要素槽位，VerifyPlan 会把它们当成被丢弃的
   * 正文，装配器也无处安放。编辑方案时必须原样保留该字段，否则会平白出现
   * 「未被任何章节收录」的错误。
   */
  elements?: ElementBinding[]
  /**
   * 正文前言：主送机关之后、第一个「一、」之前的内容（对应
   * docmodel.Plan.PreambleNodeIDs）。
   *
   * 几乎所有真实公文都以这样一段开头 ——「为深入贯彻落实……，现将有关事项
   * 通知如下。」它属于正文，却不属于任何章节：若硬塞进章节，就会凭空印出一个
   * 作者没写过的标题（如「一、通知引言」），还会占掉第一个真实标题应有的层次序数。
   *
   * 界面必须把它呈现为「正文前言（无编号）」，**不显示成章节、不编号**。
   */
  preamble_node_ids?: string[]
  sections: Section[]
  /**
   * 已声明的改动（对应 docmodel.Plan.Edits）。
   *
   * 原设计完全不允许改动内容，那是最安全的规则，却让工具对用户真正拿来的文档
   * 毫无用处 —— 带错别字、病句、或某章本就没有标题的稿子。规则因此放宽为：
   * 原文可以被修改，但每一处修改都必须显式声明、可见、可审计。未声明的改动
   * 仍然是错误（VerifyVerbatim 会抓到），所以没有任何东西能被偷偷改写。
   */
  edits?: Edit[]
  /** 已声明的整段重组（对应 docmodel.Plan.Recompose）。 */
  recompose?: Recompose[]
  notes?: string[]
  model?: string
}

/** ElementBinding 把一个原文节点绑到一个公文要素槽位。 */
export interface ElementBinding {
  /** 槽位：title / recipient / date / cc / …；不得为 body。 */
  role: Role
  /** 填充该槽位的原文节点 id。 */
  node_id: string
}

/** 可绑定为公文要素的槽位，与 docmodel.BindableRoles() 一致（不含 body）。 */
export const BINDABLE_ROLES: Role[] = [
  'copy_number',
  'security',
  'urgency',
  'masthead',
  'doc_number',
  'signatory',
  'title',
  'recipient',
  'attachment_note',
  'signature',
  'date',
  'note',
  'cc',
  'printer',
]

/** Diagnostic 是一条校验发现。code 稳定，UI 不应去匹配中文。 */
export interface Diagnostic {
  code: string
  severity: Severity
  message: string
  node_ids?: string[]
}

/** Report 是 VerifyPlan 的结论。 */
export interface Report {
  diagnostics: Diagnostic[]
  /** 原文中携带正文的节点总数。 */
  content_nodes: number
  /** 方案覆盖到的节点数。 */
  covered_nodes: number
  /** 被重复收录的节点数。 */
  duplicated_nodes: number
  /** 指向不存在节点的引用数。 */
  unknown_refs: number
  /** 节内顺序与原文不一致的章节数。 */
  reordered_sections: number
  /**
   * 章节之间的顺序与原文不一致的章节数。
   *
   * 与 reordered_sections 不同：后者只看一节之内，这里看整篇——把「二、」
   * 整章放到「一、」前面也一样能通过节内检查。可导出，但会让读者看到
   * 「二、」排在「一、」之前。
   */
  sections_out_of_order?: number
  /** 模型自造标题数。 */
  synthesized_headings: number
  /** 被声明重组的原文段落数。 */
  recomposed_nodes?: number
  /** 重组后产生的段落总数。 */
  recomposed_parts?: number
  /** 重组后字数明显少于原文的段落数（需人工确认没有删信息）。 */
  heavy_recompose?: number
  /** 绑定到公文要素槽位的节点数（对应 docmodel.Report.BoundElements）。 */
  bound_elements: number
}

/**
 * AppliedEdit 记录一处已生效的改动（对应 docmodel.AppliedEdit）。
 *
 * 它不是失败，而是用户复核用的改动日志：只有真正落到成文里的改动才会出现。
 */
export interface AppliedEdit {
  node_id: string
  kind: string
  /** 中文标签，由服务端 EditLabel() 生成。 */
  label: string
  before: string
  after: string
  reason?: string
}

/**
 * UnappliedEdit 记录一处未能生效的改动（对应 docmodel.UnappliedEdit）。
 *
 * 成因是 before 在原文中找不到（模型记错了字）或目标节点不存在。它不构成
 * 文档本身的失败（原文完好无损），但用户必须知道「模型想做而没做成」。
 */
export interface UnappliedEdit {
  node_id?: string
  kind: string
  before: string
  reason: string
}

/** VerbatimDiff 说明成文相对原文的偏差。 */
export interface VerbatimDiff {
  /** 输出中缺失的原文。 */
  missing?: string[]
  /** 输出中多出的文本（应全部是装配器生成的脚手架）。 */
  extra?: string[]
  /**
   * 同一 node id 下文本被改写、且**没有**已声明改动覆盖的条目 —— 必须恒为空。
   *
   * 这就是「偷偷改写」的定义，也是 Clean() 判定的两项之一（另一项是 missing）。
   */
  altered?: string[]
  /** 已生效的改动（审核用，不是失败）。 */
  changed?: AppliedEdit[]
  /** 未生效的改动（before 未找到）。 */
  unapplied?: UnappliedEdit[]
  /** 每处重写的改动幅度与事实校验，供校对区逐条展示。 */
  rewrites?: RewriteCheck[]
}

/**
 * RewriteCheck 是一处「整段重写」的审计记录（对应 docmodel.RewriteCheck）。
 *
 * 关键点：`change` 是实测的改动幅度，`budget` 是这类文本允许的上限。
 * `over_budget` 为 true 时服务端会拒绝导出——所以界面必须把它显示出来，
 * 否则用户只看到「导出失败」却不知道哪一段改多了。
 */
export interface RewriteCheck {
  node_id: string
  /** formal / mixed / colloquial。 */
  tier: string
  /** 中文口径名：已规范 / 一般书面 / 偏口语。 */
  tier_label: string
  budget: number
  change: number
  over_budget?: boolean
  /** 改后丢失的原文字段（number/date/docno 为硬事实，必须为空）。 */
  missing_facts?: FactLoss[]
  /** 改后出现、原文没有的数字类内容。 */
  invented_facts?: FactLoss[]
  reason?: string
}

/** FactLoss 是重写未保住（或凭空添加）的一处原文数据。 */
export interface FactLoss {
  kind: string
  text: string
  hard: boolean
}

/**
 * VerbatimDiff.Clean()：无缺失、无未声明改写、无超预算重写。
 * **已声明且未超预算的改动不算失败**。
 */
export function diffClean(d: VerbatimDiff | null | undefined): boolean {
  if (!d) return false
  if ((d.missing?.length ?? 0) > 0 || (d.altered?.length ?? 0) > 0) return false
  return !(d.rewrites ?? []).some(
    (r) => r.over_budget || (r.missing_facts ?? []).some((f) => f.hard),
  )
}

// ---------------------------------------------------------------- gbstandard

/** PageSetup 是公文用纸的版面几何（标准值）。 */
export interface PageSetup {
  width_mm: number
  height_mm: number
  margin_top_mm: number
  margin_bottom_mm: number
  margin_left_mm: number
  margin_right_mm: number
  lines_per_page: number
  chars_per_line: number
  header_mm: number
  footer_mm: number
}

/** BorderRule 描述一条横线。 */
export interface BorderRule {
  color: string
  size_eighth_pt: number
  space_pt: number
  css_width: string
}

/** ParaStyle 是一个公文元素的完整排版参数。 */
export interface ParaStyle {
  role: Role
  name: string
  font: string
  latin_font: string
  size: FontSize
  bold: boolean
  color: string
  align: Align
  line_rule: LineRule
  line_spacing_pt: number
  space_before_lines: number
  space_after_lines: number
  first_line_indent_chars: number
  left_indent_chars: number
  right_indent_chars: number
  hanging_indent_chars: number
  keep_with_next: boolean
  page_break_before?: boolean
  border_bottom?: BorderRule
  border_top?: BorderRule
  outline_level?: number
  standard?: string
}

/** Spec 是完整的格式标准：版面 + 每个 role 一条样式。 */
export interface Spec {
  page: PageSetup
  direction: Direction
  styles: Record<Role, ParaStyle>
  page_number_size: FontSize
  page_number_font: string
  page_number_dash: string
  /**
   * 体裁结构：这种文稿应当有哪些要素、标题怎么编号、最多几级。
   *
   * 旧配置里没有这一块，读取时用 defaultOutline() 补齐 —— 服务端也会做同样
   * 的归一化，所以界面上看到的默认值与真正生效的规则一致。
   */
  outline?: Outline
}

/** HeadingForm 是一级标题的编号形式，例如中文数字 + 「、」。 */
export interface HeadingForm {
  /** chinese = 一、二、；arabic = 1、2。 */
  numeral: 'chinese' | 'arabic'
  /** 数字前面的字符，例如「（」。 */
  prefix?: string
  /** 数字后面的字符，例如「、」。 */
  suffix?: string
}

/**
 * Outline 描述一类文稿的结构规则（对应 gbstandard.Outline）。
 *
 * 它和排版规则放在同一份 profile 里：换一种文体，既是换字体字号，也是换
 * 「要不要主送机关、章节怎么编号、允许多少级」。
 */
export interface Outline {
  /** 必须出现的要素；缺了只提示，不阻止导出（很多文稿本来就没有抄送）。 */
  required_elements?: Role[]
  /** 每一级的编号形式，下标 0 为一级标题。 */
  heading_forms?: HeadingForm[]
  max_depth: number
  /** 是否应有一段不带编号的正文前言。 */
  preamble: boolean
  /** 主送机关是否必填。 */
  requires_recipient: boolean
  /** 语言风格要求，会原样写进模型提示词。 */
  tone?: string
  /** 这类文稿的附加要求（本单位习惯、行业规范等）。 */
  guidance?: string
}

/** 公文默认体裁结构，与服务端 gbstandard.DefaultOutline 保持一致。 */
export function defaultOutline(): Outline {
  return {
    required_elements: ['title', 'recipient', 'date'],
    heading_forms: [
      { numeral: 'chinese', suffix: '、' },
      { numeral: 'chinese', prefix: '（', suffix: '）' },
      { numeral: 'arabic', suffix: '.' },
      { numeral: 'arabic', prefix: '（', suffix: '）' },
    ],
    max_depth: 4,
    preamble: true,
    requires_recipient: true,
    tone: '庄重、简明、规范的公文语体；不口语化，不使用第一人称叙述，不添加原文没有的事实与承诺',
    guidance: '',
  }
}

/** 取一份可安全编辑的体裁结构；旧配置没有 outline 时返回公文默认值。 */
export function outlineOf(spec: Spec | undefined | null): Outline {
  const d = defaultOutline()
  const o = spec?.outline
  if (!o) return d
  const forms =
    o.heading_forms && o.heading_forms.length > 0
      ? o.heading_forms.map((f) => ({ ...f }))
      : d.heading_forms!.map((f) => ({ ...f }))
  const depth = Math.max(1, o.max_depth || forms.length)
  // 每一级都要有编号形式：旧配置可能只有两级，增加层级时不能留下空编号。
  while (forms.length < depth) {
    const fallback = d.heading_forms![forms.length] ?? { numeral: 'arabic', suffix: '.' }
    forms.push({ ...fallback })
  }
  return {
    ...d,
    ...o,
    required_elements: o.required_elements ?? d.required_elements,
    heading_forms: forms,
    max_depth: Math.min(depth, forms.length),
  }
}

// ---------------------------------------------------------------- profile

/**
 * Profile 是一套命名的排版规则（对应 api.Profile）。
 *
 * 软件因此是通用的：公文只是其中一套内置规则，用户还可以自建论文、报告等。
 * 换一套 profile 就换掉标题/正文/行间距/字体/字号/页边距 —— 渲染管线不变，
 * 变的只是这张规则表。
 */
export interface Profile {
  id: string
  name: string
  description?: string
  /**
   * 是否内置。内置规则**不可直接改、不可删**，只能「复制一份」再改：
   * 它是所有文档的兜底规则，也是恢复到标准样式的唯一依据。
   *
   * Go 侧是 `omitempty`，所以内置=false 时该字段**根本不存在**，
   * 因此判读必须用真值判断而不是 `=== false`。
   */
  built_in?: boolean
  spec: Spec
}

/** ProfilesResponse 是配置列表与当前默认。 */
export interface ProfilesResponse {
  profiles: Profile[]
  active: string
}

/** ProfileRequest 新建或更新一套规则。 */
export interface ProfileRequest {
  /** 更新时必填；新建时必须为空（新建由服务端分配 id）。 */
  id?: string
  name: string
  description?: string
  spec: Spec
  /** 从已有配置复制规则；此时 spec 传空对象。 */
  copy_from?: string
  /** 同时设为新上传文档的默认。 */
  activate?: boolean
}

/**
 * 中文标准字号的磅值，与 internal/gbstandard 的 fontPoints 逐项一致。
 *
 * 界面用它把「三号」翻译成「16pt」给人看。用户是按字号名思考的，不是按磅值，
 * 所以下拉里给名字、旁边给磅值，两者都要有。
 */
export const FONT_SIZE_POINTS: Record<FontSize, number> = {
  初号: 42,
  小初: 36,
  一号: 26,
  小一: 24,
  二号: 22,
  小二: 18,
  三号: 16,
  小三: 15,
  四号: 14,
  小四: 12,
  五号: 10.5,
  小五: 9,
}

/** 字号下拉的顺序：从大到小，与 Word 的中文排版习惯一致。 */
export const FONT_SIZE_ORDER: FontSize[] = [
  '初号',
  '小初',
  '一号',
  '小一',
  '二号',
  '小二',
  '三号',
  '小三',
  '四号',
  '小四',
  '五号',
  '小五',
]

/** 是否是标准字号。服务端也校验这一条，但本地先拦一道能少一次往返。 */
export function isKnownFontSize(v: string): v is FontSize {
  return Object.prototype.hasOwnProperty.call(FONT_SIZE_POINTS, v)
}

/**
 * 字体候选。
 *
 * 前六项是公文用字，也是各内置配置的来源；后面几项是通用中文排版常备字体，
 * 让用户建「论文/报告」这类配置时不必自己猜名字。
 */
export const FONT_CHOICES: string[] = [
  '仿宋_GB2312',
  '黑体',
  '楷体_GB2312',
  '方正小标宋简体',
  '宋体',
  'Times New Roman',
  '仿宋',
  '楷体',
  'Microsoft YaHei',
  '微软雅黑',
  'SimSun',
  'SimHei',
  'KaiTi',
  'STSong',
  'Arial',
]

/**
 * 规则编辑器里的 role 顺序。
 *
 * 它按「读者看到这份文件的顺序」排，而不是按 role 常量排：先是页面级元素，
 * 再是标题与各级小标题，然后正文，最后是尾部要素。用户照这个顺序改，
 * 脑子里就是文档的样子。
 */
export const EDITOR_ROLE_ORDER: Role[] = [
  'copy_number',
  'security',
  'urgency',
  'masthead',
  'doc_number',
  'signatory',
  'red_line',
  'title',
  'recipient',
  'body',
  'heading1',
  'heading2',
  'heading3',
  'heading4',
  'attachment_note',
  'signature',
  'date',
  'note',
  'cc',
  'printer',
  'attachment',
]

/**
 * 规则编辑器默认展开的 role。
 *
 * 只展开标题、正文和前三级标题：它们是用户调版式时真正会动的部分，也是
 * 「换个配置，看起来不一样」的来源。其余 role 折叠起来，避免一张表单
 * 二十多行把页面撑得没法用。
 */
export const EDITOR_ROLE_PRIMARY: Role[] = [
  'title',
  'body',
  'heading1',
  'heading2',
  'heading3',
]

// ---------------------------------------------------------------- api

/** ErrorDetail 是一次失败的机器可读描述。 */
export interface ErrorDetail {
  code: string
  message: string
  details?: string[]
}

/** ErrorBody 是所有失败响应的统一外形。 */
export interface ErrorBody {
  error: ErrorDetail
}

/** 稳定的错误码。 */
export const ErrorCode = {
  badRequest: 'bad_request',
  notFound: 'not_found',
  badKey: 'bad_key',
  upstream: 'upstream_error',
  noLLMConfig: 'llm_not_configured',
  invalidPlan: 'invalid_plan',
  parseFailed: 'parse_failed',
  renderFailed: 'render_failed',
  internal: 'internal_error',
} as const

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode]

/** Health 是存活探针。 */
export interface Health {
  status: string
  version: string
  llm_configured: boolean
}

/** LLMConfig 是模型连接配置。api_key 只写不回显。 */
export interface LLMConfig {
  endpoint: string
  model: string
  protocol: string
  api_key_set: boolean
  /** 仅写入时提交；读取时后端不会回传。 */
  api_key?: string
}

/** ModelListRequest 探测尚未保存的连接可用的模型。 */
export interface ModelListRequest {
  endpoint: string
  protocol?: string
  /** 接口地址未变时可省略，服务端复用已保存的 Key。 */
  api_key?: string
}

/** ModelListResponse 是接口返回的模型 id 列表。 */
export interface ModelListResponse {
  models: string[]
}

/** ConfigResponse 是完整应用配置。 */
export interface ConfigResponse {
  llm: LLMConfig
  direction: Direction
  spec: Spec
  /** 是否允许模型修正错别字/语病/用词（服务端保存的偏好）。 */
  allow_edits: boolean
  /** 各类文本允许模型改动的百分比上限。 */
  rewrite: RewriteLimits
  /** 新上传文档默认使用哪套排版规则。 */
  active_profile?: string
  /** 全部可用规则，设置页无需第二次请求即可渲染列表。 */
  profiles?: Profile[]
  /**
   * 是否支持把设置写入磁盘。为 false 时界面**不得**承诺「重启后仍然有效」。
   */
  persistent: boolean
  /** 设置文件路径，让「重启后仍有效」变成用户可核实的事实。 */
  settings_path?: string
  /** 上一次保存是否真的落盘。 */
  saved_to_disk: boolean
  /** 落盘失败时的中文说明；此时设置本次运行仍生效，属警告而非错误。 */
  save_warning?: string
}

/** ConfigRequest 更新配置；省略的字段保持不变。 */
export interface ConfigRequest {
  endpoint?: string
  model?: string
  protocol?: string
  /** 只在用户真的输入了新密钥时提交。 */
  api_key?: string
  direction?: Direction
  /** 是否允许模型修正正文内容。 */
  allow_edits?: boolean
  /** 更新改写幅度上限。缺省表示不改。 */
  rewrite?: RewriteLimits
}

/**
 * RewriteLimits 是「某类文本最多能改多少」的配置（对应 style.Limits）。
 *
 * 三个数字按原文语言的口语程度分级：已经规范的公文只能小改，明显口语的
 * 才允许大改。这正是「尽量少改动」的具体执行标准。
 */
export interface RewriteLimits {
  enabled: boolean
  formal_max: number
  mixed_max: number
  colloquial_max: number
}

/** FontStatus 报告某款公文字体在本机是否可用。 */
export interface FontStatus {
  name: string
  available: boolean
  fallbacks: string[]
  used: string[]
}

/** SpecResponse 是当前格式标准 + 字体诊断。 */
export interface SpecResponse {
  spec: Spec
  fonts: FontStatus[]
  /** 非空说明 spec 不可信，UI 应拒绝导出。 */
  problems?: string[]
}

/** NodeSummary 是上传响应与原文面板使用的轻量节点视图。 */
export interface NodeSummary {
  id: string
  kind: NodeKind
  level?: number
  text: string
  order: number
  source_style?: string
  source_page?: number
  has_image?: boolean
  table_cells?: number
}

/** UploadResponse 由 POST /api/documents 返回。 */
export interface UploadResponse {
  doc_id: string
  source_name: string
  format: string
  node_count: number
  content_count: number
  /**
   * 本文档**实际**生效的排版规则 id。
   *
   * 界面必须显示它，而不是用户在下拉框里选的那个：服务端对未知值会静默回退到
   * 当前默认，若界面显示用户的选择，用户就会以为用了一套其实没用的规则。
   */
  profile_id?: string
  nodes: NodeSummary[]
}

/** DocumentResponse 包装完整源文档。 */
export interface DocumentResponse {
  doc_id: string
  doc: DocumentModel
  /** 该文档记住的排版规则 id。 */
  profile_id?: string
}

/** PlanRequest 请求开始分析。 */
export interface PlanRequest {
  hint?: string
  direction?: Direction
  /**
   * 是否允许模型修正错别字/语病/用词。
   *
   * 省略表示沿用服务端保存的偏好；显式传布尔值则本次分析覆盖它。开启它并不
   * 意味着可以「偷偷改写」：模型的每一处修正都必须作为一条 Edit 声明出来。
   */
  allow_edits?: boolean
  /**
   * 不调用模型，直接按原文自带的层次序数切出结构。
   *
   * 用于没有配置模型、或模型不可用时仍能出一份可用的初稿——前提是原文本来
   * 就编了号。它不会凭空造标题，因此结果是一份可以手工调整的起点，而不是
   * "分析结论"。
   */
  baseline?: boolean
}

/** PlanJobResponse 在分析启动时返回，随后客户端跟随 SSE 流。 */
export interface PlanJobResponse {
  job_id: string
}

/** StageEvent 是一帧 SSE 进度。 */
export interface StageEvent {
  name: string
  message: string
  done: boolean
}

/**
 * PartialEvent 是模型生成过程中的临时快照。
 *
 * 方案尚未完成，也没有通过校验，只能用于“先把已经生成的部分显示出来”。
 * 最终能否预览和导出，只由终态 DoneEvent 的 report 决定。
 */
export interface PartialEvent {
  plan?: Plan
  edits?: Edit[]
  reset?: boolean
}

/** PlanResponse 让方案与校验结论永远一起传输。 */
/**
 * 结构差异中的一处移动。
 *
 * 对应 internal/baseline.Move：一段原文在成品里换了归属章节。它是**结构**层面的
 * 改动日志，与 Edits（文字层面的改动日志）分工相同：VerifyPlan 只保证"没有丢、
 * 没有重复"，移动本身它看不见。
 */
export interface StructureMove {
  node_id: string
  /** 原文摘录，供界面直接展示。 */
  text: string
  /** moved | to_preamble | from_preamble | to_element */
  kind: string
  from: string
  from_id: string
  to: string
  to_id: string
  /**
   * 汇总条目的段落数。
   *
   * 原文完全没有分章时，各段归入哪一章不是原文能回答的问题；界面按目标
   * 章节汇总成一条（此时 node_id 为空），避免几十张一模一样的卡片。
   */
  count?: number
}

/** 一个章节相对原文结构的增删改名。 */
export interface SectionChange {
  /** added | missing | retitled | releveled | reparented | synthesized */
  kind: string
  id: string
  title: string
  baseline_title?: string
  /** 该章对应的原文标题节点；用于「恢复这一章」。 */
  heading_node_id?: string
  /** 方案里的层级。 */
  level?: number
  /** 原文里的层级。 */
  baseline_level?: number
  /** 方案里的父章节名称（顶层为空时显示「顶层」）。 */
  parent?: string
  /** 原文里的父章节名称。 */
  baseline_parent?: string
  /** 方案里父章节的原文标题节点，用于一键挂回。 */
  parent_heading_node_id?: string
  /** 原文里父章节的标题节点。 */
  baseline_parent_heading_node_id?: string
}

/** 一个章节在阅读顺序中的位置变化（1-based）。 */
export interface SectionMove {
  id: string
  title: string
  baseline_pos: number
  rendered_pos: number
}

/**
 * 方案与「原文自身结构」的差异。
 *
 * 基线是**不看模型**、直接按原文自带的层次序数切出来的方案。拿模型方案与它比对，
 * 就能列出"哪几段换了章节、哪几个章节改了名或换了位置"——这是让结构判断也变成
 * 可见、可核对的事实，而不是只能靠通读原稿发现。
 */
export interface StructureDiff {
  moves: StructureMove[]
  sections: SectionChange[]
  moved_sections: SectionMove[]
  /** 方案把某段绑定成公文要素（标题/主送机关/成文日期…），而原文结构里没有。 */
  elements?: ElementChange[]
  /** 模型自己写的公文要素值，原文没有对应段落。 */
  meta?: MetaChange[]
  /** 章节内（或正文前言内）正文顺序与原文不一致。 */
  body_orders?: BodyOrder[]
  /**
   * 原文自身没有任何章节结构（基线只有一个不分章的桶）。
   *
   * 此时模型分章会让每一段都「换了章节」，那正是要达成的结果，不应逐段弹卡；
   * 界面把这类移动按目标章节汇总成一条。
   */
  baseline_flat?: boolean
}

/** 一处要素绑定：某段原文被放到版头/版记的槽位。 */
export interface ElementChange {
  /**
   * bound = 新增绑定（这段原文被放进了版头/版记）；
   * unbound = 原文有的绑定被取消（例如标题被当成普通正文）。
   */
  kind: string
  role: string
  node_id: string
  text: string
}

/** 一处模型自拟的公文要素（原文没有对应段落）。 */
export interface MetaChange {
  field: string
  label: string
  value: string
}

/** 一节正文（或前言）的段序与原文不一致。 */
export interface BodyOrder {
  /** section | preamble */
  kind: string
  section_id?: string
  title: string
  node_ids: string[]
  /** 本节还含重组分片，无法只靠排序正文 id 还原。 */
  has_parts?: boolean
}

export interface PlanResponse {
  plan: Plan | null
  report: Report | null
  /** 相对原文自身结构的差异；无差异时为空数组。 */
  structure?: StructureDiff
  job_id?: string
}

/** ReviseRequest 是工作台对话的一次修改要求。 */
export interface ReviseRequest {
  instruction: string
  /** 用户在预览里选中的文字；为空表示针对整篇。 */
  selection?: string
  /** 选中文字来自哪个原文节点（可确定时）。 */
  node_id?: string
  allow_edits?: boolean
}

/** ReviseResponse 是模型给出的待审阅改动与一句回复。 */
export interface ReviseResponse {
  edits: Edit[]
  reply?: string
}

/** DoneEvent 是终止 SSE 帧。 */
export interface DoneEvent {
  plan: Plan | null
  report: Report | null
  structure?: StructureDiff
  error?: string
}

/** RenderRequest 请求成文输出。 */
export interface RenderRequest {
  format: 'docx'
  /** 用户最新编辑的方案；省略时服务端使用已存方案。 */
  plan?: Plan
}

/** RenderResponse 报告成文结果。 */
export interface RenderResponse {
  file_id: string
  filename: string
  size: number
  report: Report | null
  diff: VerbatimDiff | null
  /** 装配器生成的脚手架文本（层次序数、版头），列出以便复核。 */
  synth_text?: string[]
}

// ---------------------------------------------------------------- UI helpers

/** kind 的中文名，用于原文节点列表。 */
export const KIND_LABEL: Record<NodeKind, string> = {
  paragraph: '段落',
  heading: '标题',
  list_item: '列表项',
  table: '表格',
  image: '图片',
  page_break: '分页符',
}

/** 只渲染一句就够的节点类型不需要展开正文。 */
export function kindCarriesProse(kind: NodeKind): boolean {
  return kind !== 'page_break'
}

/** role 的中文名，标题样式表里也可能缺，故是可选的。 */
export const ROLE_LABEL: Partial<Record<Role, string>> = {
  copy_number: '份号',
  security: '密级',
  urgency: '紧急程度',
  masthead: '发文机关标志',
  doc_number: '发文字号',
  signatory: '签发人',
  red_line: '红色分隔线',
  title: '标题',
  recipient: '主送机关',
  body: '正文',
  heading1: '一级标题',
  heading2: '二级标题',
  heading3: '三级标题',
  heading4: '四级标题',
  attachment_note: '附件说明',
  signature: '发文机关署名',
  date: '成文日期',
  note: '附注',
  cc: '抄送机关',
  printer: '印发机关',
  attachment: '附件',
}

/** severity 的中文名。 */
export const SEVERITY_LABEL: Record<Severity, string> = {
  error: '错误',
  warning: '警告',
  info: '提示',
}

/**
 * 一套规则的一句话摘要，让用户不打开编辑器就能看出两套配置差在哪。
 *
 * 用户挑 profile 的方式就是「我要那种正文小一点、页边距宽一点的」，
 * 所以摘要必须直接给正文那几项与页边距 —— 把二十多个 role 全列出来
 * 既没有可比性也读不下去。
 */
export function specSummary(spec: Spec | undefined | null): string {
  if (!spec) return '规则无法读取'
  const body = spec.styles?.body
  const title = spec.styles?.title
  const p = spec.page
  const bits: string[] = []
  if (body) {
    bits.push(`正文 ${body.font} ${body.size}`)
    // 行距对「看起来不一样」影响很大，必须露出来。
    if (body.line_spacing_pt > 0) bits.push(`行距 ${trimNum(body.line_spacing_pt)}pt`)
    if (body.first_line_indent_chars > 0) {
      bits.push(`首行缩进 ${trimNum(body.first_line_indent_chars)} 字`)
    }
  }
  if (title) bits.push(`标题 ${title.size}`)
  if (p) {
    bits.push(`页边距 ${trimNum(p.margin_top_mm)}/${trimNum(p.margin_bottom_mm)}/${trimNum(p.margin_left_mm)}/${trimNum(p.margin_right_mm)}mm`)
  }
  return bits.length > 0 ? bits.join(' · ') : '无规则数据'
}

/** 去掉无意义的小数尾巴（28.990694… → 28.99）。 */
export function trimNum(v: number): string {
  if (!Number.isFinite(v)) return '—'
  return String(Math.round(v * 100) / 100)
}

/** 由 profile id 找名称；找不到时回显 id 本身而不是空白。 */
export function profileName(profiles: Profile[] | undefined, id: string | undefined): string {
  if (!id) return ''
  return profiles?.find((p) => p.id === id)?.name ?? id
}

/**
 * Report.OK()：无 error 且每个正文节点恰好被收录一次才可成文。
 *
 * 对 `diagnostics` 做了空值防护：Go 的 nil slice 编码成 JSON `null`，而校验
 * **通过**时它恰好为空 —— 不设防会在「用户终于修好」那一刻崩掉界面。
 */
export function reportOK(r: Report | null | undefined): boolean {
  if (!r) return false
  if (r.duplicated_nodes > 0 || r.unknown_refs > 0) return false
  if (r.covered_nodes !== r.content_nodes) return false
  return !(r.diagnostics ?? []).some((d) => d.severity === 'error')
}

/** Report 的中文一行摘要，与 Go 端 Summary() 等价。 */
export function reportSummary(r: Report | null | undefined): string {
  if (!r) return '尚未校验'
  return `${reportOK(r) ? '校验通过' : '校验未通过'}：正文节点 ${r.covered_nodes}/${r.content_nodes} 已覆盖，重复 ${r.duplicated_nodes}，无效引用 ${r.unknown_refs}`
}
