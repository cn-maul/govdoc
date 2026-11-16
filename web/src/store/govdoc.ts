/**
 * GovDoc 前端状态中心。
 *
 * 用一个模块级 reactive 对象承载全部跨区状态，而不是引入额外状态库：
 * 三个区域共享的是同一份「当前文档 / 当前方案」，集中在一处比分散的
 * store 更容易看出「什么时候会把方案提交回服务端」。
 *
 * 关于原文不变的约定：本文件从不修改源节点的 text。方案编辑只改章节的
 * title / body_node_ids / heading_node_id，原文永远只从服务端读取。
 */

import { computed, reactive } from './reactive'
import { ApiError, api, describeError, triggerDownload } from '../api/client'
import {
  diffClean,
  reportOK,
  type ConfigResponse,
  type Direction,
  type DocumentResponse,
  type Edit,
  type Health,
  type Plan,
  type PartialEvent,
  type PlanResponse,
  type Profile,
  type ProfileRequest,
  type ProfilesResponse,
  type Recompose,
  type RenderResponse,
  type Report,
  type SpecResponse,
  type StageEvent,
  type StructureDiff,
  type UploadResponse,
} from '../api/types'
import { clonePlan } from '../lib/tree'
import { addEdit, removeEditAt } from '../lib/edits'
import {
  applyRecompose,
  splitRecompose,
  type PendingRecompose,
} from '../lib/recompose'

/**
 * 区域标识。
 *
 * `settings` 不属于左侧的 4 步工作流：它是应用级设置页，从右上角的状态按钮
 * （以及上传区的「管理规则」）进入，整页独立，不占工作流的步骤。
 */
export type TabKey = 'upload' | 'settings'

interface State {
  // 基础信息
  health: Health | null
  config: ConfigResponse | null
  specInfo: SpecResponse | null
  booting: boolean
  bootError: string

  // 排版规则（profile）
  profiles: Profile[]
  activeProfile: string
  /** 正在提交某个 profile 操作（新建/保存/删除/设为默认）。 */
  savingProfile: boolean

  // 上传与配置
  upload: UploadResponse | null
  source: DocumentResponse | null
  uploading: boolean
  savingConfig: boolean
  /** 上传时用户选择的 profile id。 */
  selectedProfile: string

  // 分析
  jobId: string
  stages: StageEvent[]
  analyzing: boolean
  analysisError: string

  // 方案与校验
  plan: Plan | null
  report: Report | null
  /**
   * 方案相对「原文自身结构」的差异。
   *
   * 与 report 互补：report 只说有没有丢、有没有重复；structure 说**哪几段换了
   * 章节**。后者只能通过与原文结构比对得出，是结构层面的改动日志。
   */
  structure: StructureDiff | null
  /** 有未提交回服务端的本地编辑。 */
  dirty: boolean
  verifying: boolean

  // 输出
  rendering: boolean
  render: RenderResponse | null
  /**
   * 用户已经触发过下载的 file_id 集合。
   *
   * 用途是把「导出成功」与「文件已拿到手」分开：导出后浏览器会自动下载一次，
   * 但用户很可能把它存到别处或取消了，所以「下载公文文档」这个主按钮必须一直
   * 留着并保持可用，而不是下载过就消失。
   */
  downloaded: string[]

  // 界面
  tab: TabKey
  /** 一次性中文提示（成功/失败），面向用户。 */
  notice: { kind: 'ok' | 'error' | 'info'; text: string } | null
  /** 工作台对话（只在本会话保留）。 */
  chat: ChatMessage[]
  /** 对话请求进行中。 */
  chatting: boolean
  /** 校对项已被用户处理过的签名；处理过的项不再显示。 */
  handledEdits: string[]
  /** 对话新建、尚未写入方案的待审改动。 */
  pendingEdits: Edit[]
  /** 模型提出、尚未写入方案的整段重组。 */
  pendingRecompose: PendingRecompose[]
  /** 最近一次处理的校对项：同意后短暂显示，让用户看到确实覆盖了原文。 */
  lastHandled: AppliedEdit | null
  /** 最近一次处理的重组。 */
  lastHandledRecompose: AppliedRecompose | null
  /** 模型仍在生成时的临时方案；只用于先显示，不参与预览/导出。 */
  partialPlan: Plan | null
  /** 模型仍在生成时的临时校对项；终态到达后由正式列表整体替换。 */
  partialEdits: Edit[]
  /** 模型仍在生成时的临时重组声明。 */
  partialRecompose: Recompose[]
}

/** 对话里的一条消息。 */
export interface ChatMessage {
  id: string
  role: 'user' | 'assistant'
  text: string
}

/** 刚刚处理的一条校对项，供校对栏显示「已覆盖/已保留」。 */
export interface AppliedEdit {
  edit: Edit
  accepted: boolean
  at: number
}

const state = reactive<State>({
  health: null,
  config: null,
  specInfo: null,
  booting: true,
  bootError: '',

  profiles: [],
  activeProfile: '',
  savingProfile: false,

  upload: null,
  source: null,
  uploading: false,
  savingConfig: false,
  selectedProfile: '',

  jobId: '',
  stages: [],
  analyzing: false,
  analysisError: '',

  plan: null,
  report: null,
  structure: null,
  dirty: false,
  verifying: false,

  rendering: false,
  render: null,
  downloaded: [],

  tab: 'upload',
  notice: null,
  chat: [],
  chatting: false,
  handledEdits: [],
  pendingEdits: [],
  pendingRecompose: [],
  lastHandled: null,
  lastHandledRecompose: null,
  partialPlan: null,
  partialEdits: [],
  partialRecompose: [],
})

/**
 * 共享状态。
 *
 * 刻意不包 readonly()：readonly 的深层类型会把 Section[] 变成
 * DeepReadonly<Section[]>，导致方案编辑函数（web/src/lib/tree.ts）无法接收。
 * 因此约定：**组件只读 gov，任何写入都必须调用本模块导出的函数**，
 * 这样「什么时候会把方案提交回服务端」始终只有一个答案。
 */
export const gov = state

/** 提示的定时器，避免连续提示堆叠。 */
let noticeTimer: number | undefined

/** 显示一条面向用户的中文提示。 */
export function notify(kind: 'ok' | 'error' | 'info', text: string): void {
  state.notice = { kind, text }
  if (noticeTimer !== undefined) window.clearTimeout(noticeTimer)
  // 错误保留更久，用户需要读；成功提示自动消失。
  noticeTimer = window.setTimeout(
    () => {
      state.notice = null
    },
    kind === 'error' ? 12000 : 4500,
  )
}

export function dismissNotice(): void {
  state.notice = null
}

/**
 * 切换到某个区域。
 *
 * 组件不直接写 `gov.tab`：切换区域会顺带滚动到页首（否则从校对区滚到一半点
 * 「设置」，设置页一打开就停在中间），这个副作用应该只在一处发生。
 */
export function showTab(key: TabKey): void {
  if (state.tab === key) return
  state.tab = key
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

/** 把异常变成中文提示并返回文本。 */
export function fail(prefix: string, err: unknown): string {
  const msg = `${prefix}：${describeError(err)}`
  // 校验失败是「预期内的结果」，由调用方决定用哪种提示，这里不重复弹。
  if (!(err instanceof ApiError && err.code === 'invalid_plan')) {
    notify('error', msg)
  }
  return msg
}

// ---------------------------------------------------------------- 启动

/** 首次加载：读取健康状态、配置、排版规则与格式标准。 */
export async function bootstrap(): Promise<void> {
  state.booting = true
  state.bootError = ''
  try {
    const [health, config, specInfo, profiles] = await Promise.all([
      api.health(),
      api.getConfig(),
      api.getSpec(),
      api.listProfiles(),
    ])
    state.health = health
    state.config = config
    state.specInfo = specInfo
    applyProfiles(profiles)
    // 上传下拉框的默认选择跟随当前默认配置。
    state.selectedProfile = state.activeProfile
  } catch (err) {
    state.bootError = describeError(err)
  } finally {
    state.booting = false
  }
}

/** 写回 profile 列表与当前默认。空列表不清空已有值，避免一次抖动把界面清空。 */
function applyProfiles(res: ProfilesResponse): void {
  state.profiles = res.profiles ?? []
  state.activeProfile = res.active || ''
  if (state.config) {
    state.config = { ...state.config, active_profile: res.active, profiles: res.profiles }
  }
}

/** 重新拉取排版规则（新建/删除/设为默认后调用）。 */
export async function refreshProfiles(): Promise<void> {
  try {
    applyProfiles(await api.listProfiles())
  } catch (err) {
    fail('读取排版规则失败', err)
  }
}

/** 重新拉取配置（保存后或需要刷新 api_key_set 时）。 */
export async function refreshConfig(): Promise<void> {
  try {
    state.config = await api.getConfig()
  } catch (err) {
    fail('读取配置失败', err)
  }
}

/**
 * 保存配置的任意子集。省略的字段保持不变。
 *
 * 模型连接与行文方向在不同页签里编辑，各保存各的；如果这里总是回传全部字段，
 * 在模型页点保存就会把「其他设置」里尚未保存的临时选择一并写掉。
 */
export async function saveConfig(input: {
  endpoint?: string
  model?: string
  protocol?: string
  apiKey?: string
  direction?: Direction
  allowEdits?: boolean
}): Promise<boolean> {
  state.savingConfig = true
  try {
    const req: Parameters<typeof api.putConfig>[0] = {}
    if (input.endpoint !== undefined) req.endpoint = input.endpoint
    if (input.model !== undefined) req.model = input.model
    if (input.protocol !== undefined) req.protocol = input.protocol
    if (input.direction !== undefined) req.direction = input.direction
    if (input.allowEdits !== undefined) req.allow_edits = input.allowEdits
    // 只有用户真的敲了新密钥才提交，否则服务端保留原密钥。
    if (input.apiKey !== undefined && input.apiKey.trim() !== '') {
      req.api_key = input.apiKey.trim()
    }

    const saved = await api.putConfig(req)
    state.config = saved
    state.health = await api.health()

    // 保存成功不等于「重启后仍然有效」：只有服务端确认已落盘才敢这么说。
    // 说得太满比不说更糟 —— 用户重启后发现配置没了，就不会再相信这个提示。
    //
    // 服务端三种状态是互斥的（已与 Lead 确认）：
    //   1. 未启用持久化：persistent=false, saved_to_disk=false, save_warning=""
    //   2. 已落盘成功：  persistent=true,  saved_to_disk=true,  save_warning=""
    //   3. 已启用但写盘失败：persistent=true, saved_to_disk=false, save_warning 非空
    //      —— 此时设置本次运行仍生效，属**警告**而非错误，因此用 error 样式的
    //      文案由 save_warning 自己给出（它已说明「本次已生效」）。
    if (saved.save_warning) {
      notify('error', saved.save_warning)
    } else if (saved.saved_to_disk && saved.persistent) {
      notify('ok', '已保存。')
    } else {
      notify('info', '已保存，本次运行内有效。')
    }
    return true
  } catch (err) {
    fail('保存配置失败', err)
    return false
  } finally {
    state.savingConfig = false
  }
}

// ---------------------------------------------------------------- 排版规则

/**
 * 复制一套规则。
 *
 * 复制走服务端的 `copy_from`，客户端不组装规则表：内置配置共二十多个 role，
 * 客户端少传一个就会被服务端以「缺少某元素的样式」拒掉，而这条信息对用户
 * 毫无意义。规则从服务端复制，天然是完整且已知的。
 */
export async function copyProfile(
  sourceId: string,
  name: string,
  description = '',
  activate = false,
): Promise<Profile | null> {
  state.savingProfile = true
  try {
    const created = await api.createProfile({
      name: name.trim(),
      description: description.trim(),
      // spec 是空的：服务端会用 copy_from 的规则填满。
      spec: {} as ProfileRequest['spec'],
      copy_from: sourceId,
      activate,
    })
    await refreshProfiles()
    notify('ok', `已复制为「${created.name}」，现在可以修改它的规则了。`)
    return created
  } catch (err) {
    fail('复制配置失败', err)
    return null
  } finally {
    state.savingProfile = false
  }
}

/** 保存一套自建规则的名称、说明与规则表。 */
export async function saveProfile(req: ProfileRequest): Promise<Profile | null> {
  state.savingProfile = true
  try {
    const saved = req.id
      ? await api.updateProfile(req.id, req)
      : await api.createProfile(req)
    await refreshProfiles()
    notify('ok', `已保存「${saved.name}」。`)
    return saved
  } catch (err) {
    // 服务端的校验说明（缺样式 / 字号非法）本身就是中文且可执行，原样展示。
    fail('保存配置失败', err)
    return null
  } finally {
    state.savingProfile = false
  }
}

/** 设为新上传文档的默认规则。已上传的文档不受影响 —— 它们各自记住自己的规则。 */
export async function setActiveProfile(id: string): Promise<boolean> {
  state.savingProfile = true
  try {
    applyProfiles(await api.activateProfile(id))
    state.selectedProfile = id
    const p = state.profiles.find((x) => x.id === id)
    notify('ok', `已把「${p?.name ?? id}」设为新文档的默认排版规则。`)
    return true
  } catch (err) {
    fail('设置默认配置失败', err)
    return false
  } finally {
    state.savingProfile = false
  }
}

/**
 * 删除一套规则。
 *
 * 内置、当前默认、或仍被已上传文档引用的都会失败 —— 这些失败是**有意**的，
 * 消息由服务端给出且已说明原因（见 profiles.go）。这里只负责把原因原样
 * 传给用户，不自己编一套说辞去替代它。
 */
export async function deleteProfile(id: string): Promise<boolean> {
  state.savingProfile = true
  try {
    await api.deleteProfile(id)
    await refreshProfiles()
    notify('ok', '已删除该配置。')
    return true
  } catch (err) {
    fail('删除配置失败', err)
    return false
  } finally {
    state.savingProfile = false
  }
}

// ---------------------------------------------------------------- 上传

/**
 * 上传文档并复位所有下游状态（换文档就必须重来）。
 *
 * `profileId` 是用户在上传区选的规则。服务端对未知值会回退到当前默认，因此
 * 界面上真正要显示的是**响应里的** `profile_id` —— 见 notify 里的分支。
 */
export async function uploadDocument(file: File, profileId?: string): Promise<boolean> {
  state.uploading = true
  state.analysisError = ''
  try {
    const chosen = profileId ?? state.selectedProfile
    const res = await api.upload(file, chosen)
    state.upload = res
    state.plan = null
    state.report = null
    state.render = null
    state.downloaded = []
    state.stages = []
    state.jobId = ''
    state.dirty = false
    state.source = null
    state.chat = []
    state.handledEdits = []
    state.pendingEdits = []
    state.pendingRecompose = []
    state.chatting = false
    state.partialPlan = null
    state.partialEdits = []

    // 用户选的那套可能不存在（比如刚被别人删了），服务端会静默回退。
    // 界面必须按实际生效的那套说话，否则用户会以为用了一套没用的规则。
    const applied = res.profile_id ?? ''
    const fellBack = chosen !== '' && applied !== chosen
    const name = state.profiles.find((p) => p.id === applied)?.name ?? applied
    if (fellBack) {
      notify(
        'info',
        `已解析「${res.source_name}」（${res.node_count} 个块，正文 ${res.content_count} 段）。` +
          `所选配置已不存在，实际改用「${name}」。`,
      )
    } else {
      notify(
        'ok',
        `已解析「${res.source_name}」（${res.node_count} 个块，正文 ${res.content_count} 段），排版规则：${name}。`,
      )
    }
    return true
  } catch (err) {
    fail('上传失败', err)
    return false
  } finally {
    state.uploading = false
  }
}

// ---------------------------------------------------------------- 工作台对话

let chatSeq = 0

/** 往对话里追加一条消息。 */
function pushChat(role: 'user' | 'assistant', text: string): void {
  chatSeq++
  state.chat = [...state.chat, { id: `m${chatSeq}`, role, text }]
}

/**
 * 把一句修改要求交给模型，并把返回的改动放进待审列表。
 *
 * 不直接改方案：返回的 edits 只是「模型的建议」，用户在校对栏逐条处理。
 * 这与服务端的契约一致 —— POST /revise 从不写方案，只有 PUT /plan 才会。
 */
export async function requestRevision(
  instruction: string,
  selection?: { text: string; nodeId?: string },
): Promise<void> {
  const docId = currentDocId()
  if (docId === '') {
    notify('error', '尚未上传文档。')
    return
  }
  const text = instruction.trim()
  if (text === '') return

  pushChat('user', text)
  state.chatting = true
  try {
    const res = await api.revise(docId, {
      instruction: text,
      selection: selection?.text,
      node_id: selection?.nodeId,
      // 这是用户主动提出的修改要求，必须允许模型给出改动；
      // 保存的「允许修正」偏好只约束自动分析。
      allow_edits: true,
    })
    const edits = res.edits ?? []
    if (edits.length === 0) {
      pushChat('assistant', res.reply?.trim() || '没有需要修改的地方。')
      return
    }
    state.pendingEdits = [...state.pendingEdits, ...edits]
    pushChat('assistant', res.reply?.trim() || `已生成 ${edits.length} 条待校对改动。`)
  } catch (err) {
    const msg = describeError(err)
    pushChat('assistant', msg)
    notify('error', msg)
  } finally {
    state.chatting = false
  }
}

/**
 * 重新分析当前文档。
 *
 * 自动分析沿用「其他设置」里的偏好；未保存过偏好时默认允许提出修订，
 * 否则校对栏永远是空的，用户会以为功能坏了。
 */
export async function reanalyze(): Promise<void> {
  const docId = currentDocId()
  if (docId === '') return
  await startAnalysis(docId, '', undefined, state.config?.allow_edits ?? true)
}

/** 移除一条待审改动（拒绝后不再显示）。 */
export function dismissPendingEdit(index: number): void {
  const edit = state.pendingEdits[index]
  state.pendingEdits = state.pendingEdits.filter((_, i) => i !== index)
  if (edit) state.lastHandled = { edit, accepted: false, at: Date.now() }
}

/**
 * 接受一条待审改动：写入方案并重新校验。
 *
 * 处理完的卡片从待审列表消失，同时留下一句结果提示。否则用户点「同意」只会
 * 看到卡片凭空不见，无法判断原文是否真的改了 —— 而「同意就覆盖」正是这块
 * 界面唯一的承诺。
 */
export async function acceptPendingEdit(index: number): Promise<boolean> {
  const edit = state.pendingEdits[index]
  if (!edit) return false
  editPlan((plan) => addEdit(plan, edit))
  // 已接受的项进入方案，不能再次以「待处理」或「原文修改」出现：
  // 校对栏只保留尚未处理的改动。
  markHandledEdit(edit)
  state.pendingEdits = state.pendingEdits.filter((_, i) => i !== index)
  const ok = await verifyPlanNow()
  state.lastHandled = { edit, accepted: true, at: Date.now() }
  return ok
}

/** 接受方案内的一条已声明改动：只做一次显式校验。 */
export async function acceptDeclaredEdit(edit: Edit): Promise<boolean> {
  markHandledEdit(edit)
  const ok = await verifyPlanNow()
  state.lastHandled = { edit, accepted: true, at: Date.now() }
  return ok
}

/**
 * 接受一条重组：把模型改写的段落与分片落点一起写回方案。
 *
 * 只写 recompose 不写 body_parts 会漏掉正文；只写 body_parts 不写
 * recompose 会被判为未声明分片。两者必须同时写回，所以走 applyRecompose。
 */
export async function acceptRecompose(index: number): Promise<boolean> {
  const pending = state.pendingRecompose[index]
  if (!pending) return false
  editPlan((plan) => applyRecompose(plan, pending))
  state.pendingRecompose = state.pendingRecompose.filter((_, i) => i !== index)
  const ok = await verifyPlanNow()
  state.lastHandledRecompose = { rec: pending.recompose, accepted: true, at: Date.now() }
  return ok
}

/** 拒绝一条重组：不写回方案，骨架方案继续显示原文。 */
export function rejectRecompose(index: number): void {
  const pending = state.pendingRecompose[index]
  state.pendingRecompose = state.pendingRecompose.filter((_, i) => i !== index)
  if (pending) {
    state.lastHandledRecompose = { rec: pending.recompose, accepted: false, at: Date.now() }
  }
}

/** 刚刚处理的一条重组，供校对栏显示结果。 */
export interface AppliedRecompose {
  rec: Recompose
  accepted: boolean
  at: number
}

/** 拒绝方案内的一条已声明改动：撤回声明并重新校验。 */
export async function rejectDeclaredEdit(edit: Edit): Promise<boolean> {
  if (!state.plan) return false
  const key = editSignature(edit)
  const index = (state.plan.edits ?? []).findIndex((e) => editSignature(e) === key)
  if (index < 0) return false
  editPlan((plan) => removeEditAt(plan, index))
  markHandledEdit(edit)
  const ok = await verifyPlanNow()
  state.lastHandled = { edit, accepted: false, at: Date.now() }
  return ok
}

/** 记住某条改动已处理，校对栏只显示未处理的。 */
export function markHandledEdit(edit: Edit): void {
  const key = editSignature(edit)
  if (!state.handledEdits.includes(key)) {
    state.handledEdits = [...state.handledEdits, key]
  }
}

/** 改动签名（与 lib/edits.ts 的 editKey 对齐）。 */
export function editSignature(edit: Edit): string {
  return [edit.node_id ?? '', edit.kind, edit.before, edit.occurrence ?? 0].join('\u0000')
}

/** 读取完整源文档（校对区需要逐字原文）。 */
export async function loadSource(docId: string): Promise<void> {
  if (state.source?.doc_id === docId) return
  try {
    state.source = await api.getDocument(docId)
  } catch (err) {
    // 服务重启后内存里的文档已不存在。继续留在工作台只会让预览、校对、导出
    // 全部报 404，用户又找不到「重新上传」的入口；直接回到上传页并说明原因。
    if (err instanceof ApiError && err.code === 'not_found') {
      resetDocument()
      notify('info', '这份文档已不在服务端（服务可能重启过），请重新上传。')
      return
    }
    fail('读取原文失败', err)
  }
}

// ---------------------------------------------------------------- 分析（SSE）

let events: EventSource | null = null

/** 关闭可能仍然打开的 SSE 连接。 */
export function closeEvents(): void {
  if (events) {
    events.close()
    events = null
  }
}

/**
 * 启动分析并跟随 SSE 进度。
 *
 * 三种终止路径都要收尾：done 帧、error 帧、连接断开。断线时回退到
 * GET /plan 拉取最终结果，避免用户看到永远转圈的进度条。
 *
 * `allowEdits` 决定模型能否修正错别字/语病/用词。它不放松「不可偷偷改写」这条
 * 底线：开启后模型的每一处修正仍必须作为一条 Edit 声明出来，界面上逐条可拒绝。
 */
export async function startAnalysis(
  docId: string,
  hint: string,
  direction?: Direction,
  allowEdits?: boolean,
  baselineOnly = false,
): Promise<void> {
  closeEvents()
  state.analyzing = true
  state.analysisError = ''
  state.stages = []
  state.partialPlan = null
  state.partialEdits = []
  state.partialRecompose = []
  state.render = null
  state.downloaded = []

  try {
    const req: {
      hint?: string
      direction?: Direction
      allow_edits?: boolean
      baseline?: boolean
    } = {}
    if (hint.trim() !== '') req.hint = hint.trim()
    if (direction) req.direction = direction
    // 省略表示沿用服务端保存的偏好；显式传值才覆盖它。
    if (allowEdits !== undefined) req.allow_edits = allowEdits
    if (baselineOnly) req.baseline = true
    const { job_id } = await api.startPlan(docId, req)
    state.jobId = job_id
  } catch (err) {
    state.analysisError = fail('无法开始分析', err)
    state.analyzing = false
    return
  }

  await new Promise<void>((resolve) => {
    const es = new EventSource(api.eventsURL(state.jobId))
    events = es
    // 一旦收到终态帧，这次分析就已经有结论了。EventSource 在服务端关闭
    // 流之后还会补发一个 error 事件；若不拦住它，兜底拉取会再取一次方案并把
    // 刚写入的失败原因覆盖掉 —— 用户看到的就变成「什么都没发生」。
    let settled = false

    const finish = (): void => {
      settled = true
      closeEvents()
      state.analyzing = false
      state.partialPlan = null
      state.partialEdits = []
      state.partialRecompose = []
      resolve()
    }

    es.addEventListener('stage', (ev) => {
      const frame = parseFrame<StageEvent>(ev)
      if (!frame) return
      // 同名 stage 就地更新，界面只显示每个阶段一行，不刷屏。
      const i = state.stages.findIndex((s) => s.name === frame.name)
      if (i >= 0) state.stages[i] = frame
      else state.stages.push(frame)
    })

    // 模型边生成边推回已完成的对象。这里只更新“预览用的临时视图”，
    // 不把它写进 plan，也不触发校验：半成品可能缺段落、缺章节，不能当成
    // 可导出的方案。
    es.addEventListener('partial', (ev) => {
      const frame = parseFrame<PartialEvent>(ev)
      if (!frame) return
      if (frame.reset) {
        state.partialPlan = null
        state.partialEdits = []
        state.partialRecompose = []
        return
      }
      if (frame.plan) state.partialPlan = normalizePlan(frame.plan)
      if (frame.edits) state.partialEdits = frame.edits
      if (frame.plan?.recompose) state.partialRecompose = frame.plan.recompose
    })

    es.addEventListener('done', (ev) => {
      const frame = parseFrame<{
        plan: Plan | null
        report: Report | null
        structure?: StructureDiff
        error?: string
      }>(ev)
      if (!frame) {
        finish()
        return
      }
      settled = true
      const staged = applyPlanResult(frame.plan, frame.report, frame.error, frame.structure)
      if (!staged || !frame.plan) {
        closeEvents()
        state.analyzing = false
        resolve()
        return
      }
      // 服务端存的是「带改动」的方案，预览会直接画出改后的文字。先把去掉
      // 改动的方案写回，左栏才与待审列表一致；接受某条时再单独写回。
      void submitPlan(docId, false).finally(finish)
    })

    es.addEventListener('error', (ev) => {
      if (settled) return
      // 服务端主动发的 error 帧带 data；EventSource 自身断线时 data 为空。
      const frame = parseFrame<{ code?: string; message?: string }>(ev as MessageEvent)
      if (frame?.message) {
        state.analysisError = `分析失败：${frame.message}`
        notify('error', state.analysisError)
        finish()
        return
      }
      if (es.readyState === EventSource.CLOSED) {
        // 连接已经彻底关闭：拉一次最终结果，可能服务端已经算完。
        //
        // CONNECTING 时必须等浏览器自动重连：分析还在跑的时候，一次短暂的
        // 网络抖动会先触发 error，此时 GET /plan 会得到 404「尚未生成结构方案」，
        // 用户就会看到一条并不真实的失败提示。
        void fallbackFetchPlan(docId).finally(finish)
      }
    })

    es.addEventListener('open', () => {
      // 只清除「连接失败」类的旧提示；模型给出的实质错误在 open 时还不知道，
      // 这里清掉会让用户看不到上一轮为什么失败。
      if (!settled && state.analysisError.startsWith('无法开始分析')) {
        state.analysisError = ''
      }
    })
  })
}

/** SSE 帧解析：data 不是合法 JSON 时返回 null 而不是抛出，避免打断整条流。 */
function parseFrame<T>(ev: Event): T | null {
  const raw = (ev as MessageEvent).data
  if (typeof raw !== 'string' || raw.trim() === '') return null
  try {
    return JSON.parse(raw) as T
  } catch {
    return null
  }
}

/** 断线兜底：直接取一次方案与校验结论。 */
async function fallbackFetchPlan(docId: string): Promise<void> {
  try {
    const res = await api.getPlan(docId)
    if (res.plan) {
      const staged = applyPlanResult(res.plan, res.report, undefined, res.structure)
      if (staged) await submitPlan(docId, false)
      return
    }
    state.analysisError = '分析连接中断，且服务端尚未产出方案，请重新分析。'
    notify('error', state.analysisError)
  } catch (err) {
    if (err instanceof ApiError && err.code === 'not_found') {
      state.analysisError = '分析连接中断，服务端没有可用的结构方案，请重新分析。'
      notify('error', state.analysisError)
      return
    }
    fail('分析连接中断后拉取结果失败', err)
    state.analysisError = describeError(err)
  }
}

/**
 * 应用一份方案与校验结论，并同步到界面。
 *
 * 注意 `error` 与 `plan` 可以同时存在：服务端的自修复循环失败时会返回
 * **部分方案**加失败原因，让用户看到模型给出了什么、为什么被拒，远好过一片空白。
 * 因此这里先收下方案，再把错误显示成提示 —— 而不是丢弃方案。
 */
export function applyPlanResult(
  plan: Plan | null,
  report: Report | null,
  error?: string,
  structure?: StructureDiff | null,
): boolean {
  if (!plan) {
    state.analysisError = error
      ? `分析失败：${error}`
      : '模型没有返回结构方案，请重试或检查模型配置。'
    notify('error', state.analysisError)
    return false
  }

  const normalized = normalizePlan(plan)
  // 模型给出的改动是**建议**，不是用户已经接受的结果。先移出方案、放进
  // 待审列表：预览必须保持原文，直到用户逐条同意。
  const incoming = normalized.edits ?? []
  const split = splitRecompose({
    ...normalized,
    edits: [],
  })
  state.plan = split.plan
  state.report = normalizeReport(report)
  state.structure = structure ?? null
  state.handledEdits = []
  state.pendingEdits = incoming
  state.pendingRecompose = split.pending
  state.lastHandled = null
  state.lastHandledRecompose = null
  state.partialPlan = null
  state.partialEdits = []
  state.partialRecompose = []
  state.dirty = false
  editRevision++
  cancelScheduledVerify()

  if (error) {
    // 有部分方案可用：不把它丢掉，而是说明「方案不完整，可在校对区手动修复」。
    state.analysisError = `模型未能一次生成合规方案：${error}`
    notify(
      'error',
      '模型未能一次生成通过校验的方案，已保留它给出的部分方案。请在校对区修复漏收/重复后导出。',
    )
    return incoming.length > 0 || split.pending.length > 0
  }

  state.analysisError = ''
  return incoming.length > 0 || split.pending.length > 0
}

/**
 * 服务端可能省略空数组，前端一律补齐，减少到处写 ?? [] 。
 *
 * 特别注意 `diagnostics`：Go 的 nil slice 会被编码成 `null`，而校验**通过**时
 * diagnostics 恰好是空的。如果原样存进状态，界面在「用户终于修好、校验通过」
 * 这一刻会因为 `null.some(...)` 直接崩掉 —— 也就是最不该崩的时刻。
 */
function normalizeReport(report: Report | null | undefined): Report | null {
  if (!report) return null
  return {
    ...report,
    diagnostics: report.diagnostics ?? [],
  }
}

/** 服务端可能省略空数组，前端一律补齐，减少到处写 ?? [] 。 */
function normalizePlan(plan: Plan): Plan {
  const fix = (
    s: Plan['sections'][number],
    path: string,
  ): Plan['sections'][number] => ({
    ...s,
    // 流式快照的章节（尤其是子章节）可能还没有 id。React 列表若用
    // undefined 作 key，会把这些行合并成一行。只在缺失时补一个稳定占位，
    // 终态方案到达后会被真实 id 覆盖。
    id: s.id || `__p${path}`,
    body_node_ids: s.body_node_ids ?? [],
    body_parts: (s.body_parts ?? []).map((bp) => ({ ...bp })),
    children: (s.children ?? []).map((child, i) => fix(child, `${path}-${i}`)),
  })
  return {
    ...plan,
    meta: plan.meta ?? {},
    // elements 必须保留：它是「这个节点就是标题/成文日期/抄送」的声明。
    elements: (plan.elements ?? []).map((e) => ({ ...e })),
    sections: (plan.sections ?? []).map((s, i) => fix(s, String(i))),
    // edits 必须保留：它是「原文这里被改过」的声明。丢掉它，那段文字在成文时
    // 就失去授权，VerifyVerbatim 会把同一处判成「未声明的改写」而拒绝导出 ——
    // 用户明明什么都没做，却突然导不出来了。
    edits: (plan.edits ?? []).map((e) => ({ ...e })),
    recompose: (plan.recompose ?? []).map((r) => ({
      ...r,
      parts: [...r.parts],
      roles: r.roles ? [...r.roles] : undefined,
    })),
    notes: plan.notes ?? [],
  }
}

// ---------------------------------------------------------------- 校验

/**
 * 编辑版本号。
 *
 * 自动校验是防抖发出的，用户可能在请求飞行途中继续敲字；若不加守卫，
 * 服务端回包的旧方案会把用户刚敲的字覆盖掉。版本号只用来决定「这次回包
 * 还该不该应用到界面」。
 */
let editRevision = 0
let verifyTimer: number | undefined
/** 最近一次自动校验失败的原因，避免防抖循环里反复弹同一个错误。 */
let lastAutoError = ''

/** 当前文档 id（上传响应是唯一来源）。 */
function currentDocId(): string {
  return state.upload?.doc_id ?? ''
}

/**
 * 对方案施加一次编辑：不就地修改、立即标记 dirty，并安排一次防抖重新校验。
 * 校对区的每一个动作（改标题、上移下移、移入其他章节）都经过这里。
 */
export function editPlan(mutate: (plan: Plan) => Plan): void {
  if (!state.plan) return
  state.plan = normalizePlan(mutate(clonePlan(state.plan)))
  state.dirty = true
  editRevision++
  scheduleVerify()
}

/** 防抖：连续编辑只在停手 700ms 后发一次 PUT。 */
function scheduleVerify(): void {
  if (verifyTimer !== undefined) window.clearTimeout(verifyTimer)
  verifyTimer = window.setTimeout(() => {
    verifyTimer = undefined
    void submitPlan(currentDocId(), false)
  }, 700)
}

/** 立刻放弃待发的自动校验（例如用户点了导出）。 */
function cancelScheduledVerify(): void {
  if (verifyTimer !== undefined) {
    window.clearTimeout(verifyTimer)
    verifyTimer = undefined
  }
}

/** 手动触发一次校验（「重新校验」按钮）。 */
export async function verifyPlanNow(): Promise<boolean> {
  cancelScheduledVerify()
  return submitPlan(currentDocId(), true)
}

/**
 * 提交本地编辑后的方案 → 服务端重新校验。
 *
 * @param explicit 用户主动点击时为 true（成功/失败都提示）；自动校验为 false
 *   （成功时静默，避免每次编辑都弹提示）。
 */
export async function submitPlan(docId: string, explicit = true): Promise<boolean> {
  if (!state.plan) return false
  if (docId === '') {
    if (explicit) notify('error', '尚未上传文档，无法校验方案。')
    return false
  }

  const revAtStart = editRevision
  const submitted = clonePlan(state.plan)
  state.verifying = true
  try {
    const res: PlanResponse = await api.putPlan(docId, submitted)
    // 编辑已继续：这次回包只用于显示，不得覆盖用户的新输入。
    if (editRevision !== revAtStart) {
      state.report = normalizeReport(res.report)
      state.structure = res.structure ?? null
      return reportOK(res.report)
    }
    if (res.plan) state.plan = normalizePlan(res.plan)
    state.report = normalizeReport(res.report)
    // 服务端每次校验都会重算结构差异（基线由原文推导，不依赖模型），所以
    // 这里必须收下——否则用户改完结构后界面上还显示旧的移动清单。
    state.structure = res.structure ?? null
    state.dirty = false
    lastAutoError = ''
    const ok = reportOK(res.report)
    if (explicit) {
      notify(ok ? 'ok' : 'info', ok ? '方案校验通过，可以导出。' : '方案已保存，但校验未通过，请看下方原因。')
    }
    return ok
  } catch (err) {
    // PUT plan 返回 invalid_plan 时服务端同时说明了原因，直接展示。
    if (err instanceof ApiError && err.code === 'invalid_plan') {
      const msg = describeError(err)
      state.analysisError = msg
      if (explicit || lastAutoError !== msg) {
        lastAutoError = msg
        notify('error', msg)
      }
      return false
    }
    fail('提交方案失败', err)
    return false
  } finally {
    state.verifying = false
  }
}

/** 标记方案被本地修改过（不触发校验时使用）。 */
export function markDirty(): void {
  state.dirty = true
  editRevision++
}

/** 用服务端返回的方案替换本地方案（例如重新分析后）。 */
export function replacePlan(plan: Plan, report: Report | null): void {
  cancelScheduledVerify()
  state.plan = normalizePlan(plan)
  state.report = normalizeReport(report)
  state.handledEdits = []
  state.pendingEdits = []
  state.pendingRecompose = []
  state.partialPlan = null
  state.partialEdits = []
  state.partialRecompose = []
  // 手工替换的方案与原文结构的差异未知，等服务端下次校验回包再填。
  state.structure = null
  state.dirty = false
  state.render = null
  editRevision++
}

/** 清空当前文档，回到首屏上传区。 */
export function resetDocument(): void {
  closeEvents()
  cancelScheduledVerify()
  state.upload = null
  state.source = null
  state.plan = null
  state.report = null
  state.structure = null
  state.lastHandled = null
  state.lastHandledRecompose = null
  state.render = null
  state.downloaded = []
  state.stages = []
  state.jobId = ''
  state.analyzing = false
  state.analysisError = ''
  state.dirty = false
  state.chat = []
  state.chatting = false
  state.handledEdits = []
  state.pendingEdits = []
  state.pendingRecompose = []
  state.partialPlan = null
  state.partialEdits = []
  state.partialRecompose = []
  showTab('upload')
}

// ---------------------------------------------------------------- 导出

/**
 * 成文并触发下载。校验未通过时直接拒绝，不把错误方案交出去。
 *
 * 成功后**立即**下载一次，并把 render 留在状态里：用户多半就是想把文件拿到手，
 * 这一步不该再让他去找按钮；同时「下载公文文档」主按钮会一直留着，因为浏览器
 * 的自动下载可能被拦截、被取消，或被存到了用户找不到的地方。
 */
export async function renderDocument(docId: string): Promise<void> {
  if (!state.plan) {
    notify('error', '尚未生成结构方案，请先分析文档。')
    return
  }
  if (state.dirty) {
    // 有未提交的编辑：先校验再导出，绝不把未校验的方案交出去。
    cancelScheduledVerify()
    const ok = await submitPlan(docId, true)
    if (!ok) return
  }
  if (!reportOK(state.report)) {
    notify('error', `方案校验未通过，无法导出。${firstErrorText()}`)
    return
  }

  state.rendering = true
  try {
    const res = await api.render(docId, { format: 'docx', plan: clonePlan(state.plan) })
    state.render = res
    state.report = normalizeReport(res.report) ?? state.report

    // 逐字比对不通过时**不自动下载**：那份文件里有未声明的内容改写，直接塞给
    // 用户等于把问题悄悄交付出去。此时把结果留在界面上，让他先看比对结论。
    if (res.diff && !diffClean(res.diff)) {
      notify('error', '成文完成，但逐字比对发现偏差，已暂停自动下载。请查看下方比对结果后再决定是否下载。')
      return
    }

    triggerDownload(api.fileURL(res.file_id), res.filename)
    markDownloaded(res.file_id)

    const applied = res.diff?.changed?.length ?? 0
    const unapplied = res.diff?.unapplied?.length ?? 0
    if (applied > 0) {
      notify(
        'ok',
        unapplied > 0
          ? `已生成「${res.filename}」并开始下载：含 ${applied} 处已声明修改；另有 ${unapplied} 处修改未能生效，请看改动汇总。`
          : `已生成「${res.filename}」并开始下载：含 ${applied} 处已声明修改，清单见下方改动汇总。`,
      )
    } else {
      notify('ok', `已生成「${res.filename}」并开始下载。`)
    }
  } catch (err) {
    fail('成文失败', err)
  } finally {
    state.rendering = false
  }
}

/** 记下这个文件已经下载过（用于「已下载」提示，按钮本身保持可用）。 */
function markDownloaded(fileId: string): void {
  if (!state.downloaded.includes(fileId)) state.downloaded = [...state.downloaded, fileId]
}

/**
 * 下载已经成文的文件（「下载公文文档」主按钮）。
 *
 * 走 GET /api/files/{file_id}，由浏览器按 Content-Disposition 处理文件名。
 * 刻意不做成 fetch+blob：那样整份文件要先在内存里过一遍，还会丢掉服务端设置
 * 的响应头，得不偿失。
 */
export function downloadRenderedFile(): void {
  const res = state.render
  if (!res) {
    notify('info', '还没有生成公文文档。请先点「生成公文文档」。')
    return
  }
  if (!reportOK(state.report)) {
    // 成文时的 report 与当前 report 可能不同（用户又改了方案），以当下的为准：
    // 方案已经不通过校验了，就不该再让用户拿走旧文件而以为它是合规的。
    notify('error', `当前方案校验未通过，无法下载。${firstErrorText()}`)
    return
  }
  triggerDownload(api.fileURL(res.file_id), res.filename)
  markDownloaded(res.file_id)
  notify('ok', `已开始下载「${res.filename}」。`)
}

/** 第一条 error 级诊断的中文说明，用于禁用导出时的提示。 */
export function firstErrorText(): string {
  const d = (state.report?.diagnostics ?? []).find((x) => x.severity === 'error')
  return d ? `原因：${d.message}` : ''
}

// ---------------------------------------------------------------- 派生状态

/** 是否已上传文档。 */
export const hasDocument = computed(() => state.upload !== null)

/** 是否已有方案。 */
export const hasPlan = computed(() => state.plan !== null)

/** 方案是否可导出（界面用它决定按钮的 disabled）。 */
export const canRender = computed(
  () => state.plan !== null && !state.dirty && reportOK(state.report) && !state.rendering,
)

/**
 * 是否已有「可下载的公文文档」。
 *
 * 两个条件缺一不可：文件已经生成，**并且**当前方案仍通过校验。只看前者会让
 * 用户在校对区把方案改坏之后，仍然从旧文件里拿到一份看起来合规的文档 ——
 * 那份文件与屏幕上显示的方案已经没有关系了。
 */
export const hasDownload = computed(() => state.render !== null && reportOK(state.report))

/** 「下载公文文档」按钮不可用时的中文原因；可用时为空串。 */
export const downloadBlockReason = computed(() => {
  if (state.render === null) return '还没有生成公文文档，请先点「生成公文文档」。'
  if (!reportOK(state.report)) {
    return `当前方案未通过校验，已暂停下载：${firstErrorText() || '请看校对区的错误清单。'}`
  }
  return ''
})

/** 这份文件是否已经被下载过（按钮仍然保持可用）。 */
export const renderedDownloaded = computed(
  () => state.render !== null && state.downloaded.includes(state.render.file_id),
)

/** 字体缺失提示。 */
export const missingFonts = computed(() => (state.specInfo?.fonts ?? []).filter((f) => !f.available))

/** 格式标准本身的问题（非空说明不应导出）。 */
export const specProblems = computed(() => state.specInfo?.problems ?? [])

/**
 * 当前文档实际生效的排版规则对象。
 *
 * 取自**上传响应**的 `profile_id`，不是用户在上传区选的那个，也不是当前默认：
 * 服务端对未知 profile 会回退到默认，所以三者是可能不同的。预览与导出都按
 * 文档记住的那套走，界面也就必须显示同一套，否则用户看到的规则解释与
 * 屏幕上的排版不是一回事。
 */
export const currentProfile = computed<Profile | null>(() => {
  const id = state.upload?.profile_id ?? ''
  if (!id) return null
  return state.profiles.find((p) => p.id === id) ?? null
})

/** 当前文档生效规则的名称；profile 列表尚未载入时回显 id。 */
export const currentProfileName = computed(() => {
  const id = state.upload?.profile_id ?? ''
  if (!id) return ''
  return currentProfile.value?.name ?? id
})
