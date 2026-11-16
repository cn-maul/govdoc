/**
 * GovDoc API 客户端。
 *
 * 所有请求都经过 request()，因此三件事只在一处保证：
 *   1. 统一解出 `{error:{code,message}}` 并抛出中文可读的 ApiError；
 *   2. 网络失败（后端未启动、断网）也变成中文提示，而不是 "Failed to fetch"；
 *   3. 非 JSON 响应（例如反代返回 HTML 错误页）不会让调用方拿到半个对象。
 */

import type {
  ConfigRequest,
  ConfigResponse,
  DocumentResponse,
  ErrorBody,
  ErrorDetail,
  Health,
  ModelListRequest,
  ModelListResponse,
  PlanRequest,
  PlanJobResponse,
  PlanResponse,
  Profile,
  ProfileRequest,
  ProfilesResponse,
  RenderRequest,
  RenderResponse,
  ReviseRequest,
  ReviseResponse,
  SpecResponse,
  UploadResponse,
} from './types'

/** API 前缀。前端总是与后端同源，故用相对路径，embed 后挂任意前缀都能用。 */
const API_BASE = 'api'

/** ApiError 携带稳定错误码与面向用户的中文消息。 */
export class ApiError extends Error {
  readonly code: string
  readonly status: number
  readonly details: string[]

  constructor(detail: ErrorDetail, status = 0) {
    super(detail.message || '请求失败')
    this.name = 'ApiError'
    this.code = detail.code || 'unknown'
    this.status = status
    this.details = detail.details ?? []
  }

  /** 面向用户的完整提示：消息 + 具体条目。 */
  get userMessage(): string {
    if (this.details.length === 0) return this.message
    return `${this.message}（${this.details.join('；')}）`
  }
}

/**
 * 错误码到中文补救建议的映射。用户看到的不应只是「失败了」，而是「下一步做什么」。
 */
const CODE_HINT: Record<string, string> = {
  bad_request: '请检查填写或选择的内容后重试。',
  not_found: '目标不存在或服务已重启，请重新上传文档。',
  bad_key: 'API Key 无效或已过期，请在「设置」页重新填写。',
  upstream_error: '模型服务返回错误，请稍后重试或更换模型/接口地址。',
  llm_not_configured: '尚未配置模型，请先到「设置」页填写接口地址、模型名与 API Key。',
  invalid_plan: '方案校验未通过，请在校对区修复红色错误后重试。',
  parse_failed: '无法解析该文件，请确认是 .docx / .txt / .md 且未加密。',
  render_failed: '成文失败，请检查方案校验结论后重试。',
  internal_error: '服务内部错误，请查看后端日志或稍后重试。',
}

/** 把任意异常翻译成面向用户的中文提示。 */
export function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    const hint = CODE_HINT[err.code]
    const base = err.userMessage
    return hint && !base.includes(hint) ? `${base} ${hint}` : base
  }
  if (err instanceof DOMException && err.name === 'AbortError') {
    return '请求已取消。'
  }
  if (err instanceof TypeError) {
    return '无法连接后端服务，请确认 govdoc 服务正在运行且页面地址正确。'
  }
  if (err instanceof Error) {
    return err.message || '发生未知错误。'
  }
  return '发生未知错误。'
}

async function parseError(res: Response): Promise<ApiError> {
  let detail: ErrorDetail = {
    code: 'internal_error',
    message: `请求失败（HTTP ${res.status} ${res.statusText}）`,
  }
  try {
    const body = (await res.json()) as Partial<ErrorBody>
    if (body && typeof body === 'object' && body.error) {
      detail = {
        code: body.error.code || detail.code,
        message: body.error.message || detail.message,
        details: body.error.details,
      }
    }
  } catch {
    // 非 JSON（网关 HTML 错误页等）：保留上面的 HTTP 状态描述。
  }
  return new ApiError(detail, res.status)
}

interface RequestOptions {
  method?: string
  /** JSON 请求体；与 body 互斥。 */
  json?: unknown
  /** 原始请求体（上传用）。 */
  body?: BodyInit
  signal?: AbortSignal
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  let body: BodyInit | undefined

  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(opts.json)
  } else if (opts.body !== undefined) {
    body = opts.body
    // 刻意不设置 Content-Type：multipart 的 boundary 必须由浏览器生成。
  }

  let res: Response
  try {
    res = await fetch(`${API_BASE}/${path.replace(/^\//, '')}`, {
      method: opts.method ?? 'GET',
      headers,
      body,
      signal: opts.signal,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new ApiError({
      code: 'network_error',
      message: '无法连接后端服务，请确认 govdoc 服务正在运行。',
    })
  }

  if (!res.ok) throw await parseError(res)

  // 204 或空响应：调用方声明 T 时才需要内容。
  const text = await res.text()
  if (text.trim() === '') return undefined as T
  try {
    return JSON.parse(text) as T
  } catch {
    throw new ApiError({
      code: 'bad_response',
      message: '后端返回了非 JSON 内容，可能访问到了错误的地址。',
    })
  }
}

// ---------------------------------------------------------------- endpoints

export const api = {
  health(signal?: AbortSignal): Promise<Health> {
    return request<Health>('health', { signal })
  },

  getConfig(signal?: AbortSignal): Promise<ConfigResponse> {
    return request<ConfigResponse>('config', { signal })
  },

  putConfig(req: ConfigRequest): Promise<ConfigResponse> {
    return request<ConfigResponse>('config', { method: 'PUT', json: req })
  },

  /** 探测当前填写的接口地址可用哪些模型，不落盘。 */
  listModels(req: ModelListRequest): Promise<ModelListResponse> {
    return request<ModelListResponse>('llm/models', { method: 'POST', json: req })
  },

  /** 把一句修改要求变成待审阅的改动；不会直接修改方案。 */
  revise(docId: string, req: ReviseRequest): Promise<ReviseResponse> {
    return request<ReviseResponse>(`documents/${docId}/revise`, { method: 'POST', json: req })
  },

  getSpec(signal?: AbortSignal): Promise<SpecResponse> {
    return request<SpecResponse>('spec', { signal })
  },

  // ---------------------------------------------------------------- profiles

  /** 列出全部排版规则与当前默认。 */
  listProfiles(signal?: AbortSignal): Promise<ProfilesResponse> {
    return request<ProfilesResponse>('profiles', { signal })
  },

  /**
   * 新建一套规则。
   *
   * `copy_from` 让复制走服务端：客户端不必回传一张自己并不完全理解的规则表，
   * 也不必担心少传了某个 role。内置配置的「复制一份再改」正是走这条路。
   */
  createProfile(req: ProfileRequest): Promise<Profile> {
    return request<Profile>('profiles', { method: 'POST', json: req })
  },

  updateProfile(id: string, req: ProfileRequest): Promise<Profile> {
    return request<Profile>(`profiles/${encodeURIComponent(id)}`, {
      method: 'PUT',
      json: { ...req, id },
    })
  },

  /** 删除一套规则。内置、当前默认、或仍被文档引用的会被服务端拒绝（400/409）。 */
  deleteProfile(id: string): Promise<void> {
    return request<void>(`profiles/${encodeURIComponent(id)}`, { method: 'DELETE' })
  },

  /** 设为新上传文档的默认规则。 */
  activateProfile(id: string): Promise<ProfilesResponse> {
    return request<ProfilesResponse>('profiles/activate', { method: 'POST', json: { id } })
  },

  /**
   * 上传文档：multipart 字段名固定为 file（契约 §3）。
   *
   * `profile` 一并提交，服务端对未知值会回退到当前默认，因此界面必须以响应里
   * 的 `profile_id` 为准，而不是以这里传进去的值为准。
   */
  upload(file: File, profileId?: string, signal?: AbortSignal): Promise<UploadResponse> {
    const form = new FormData()
    form.append('file', file, file.name)
    if (profileId) form.append('profile', profileId)
    return request<UploadResponse>('documents', { method: 'POST', body: form, signal })
  },

  getDocument(docId: string, signal?: AbortSignal): Promise<DocumentResponse> {
    return request<DocumentResponse>(`documents/${encodeURIComponent(docId)}`, { signal })
  },

  /** 启动 LLM 分析，返回 job_id；进度随后从 SSE 流读取。 */
  startPlan(docId: string, req: PlanRequest = {}): Promise<PlanJobResponse> {
    return request<PlanJobResponse>(
      `documents/${encodeURIComponent(docId)}/plan`,
      { method: 'POST', json: req },
    )
  },

  getPlan(docId: string, signal?: AbortSignal): Promise<PlanResponse> {
    return request<PlanResponse>(`documents/${encodeURIComponent(docId)}/plan`, { signal })
  },

  /** 提交用户编辑后的方案 → 重新校验 → 返回新的 plan + report。 */
  putPlan(docId: string, plan: PlanResponse['plan']): Promise<PlanResponse> {
    return request<PlanResponse>(
      `documents/${encodeURIComponent(docId)}/plan`,
      { method: 'PUT', json: plan },
    )
  },

  render(docId: string, req: RenderRequest): Promise<RenderResponse> {
    return request<RenderResponse>(
      `documents/${encodeURIComponent(docId)}/render`,
      { method: 'POST', json: req },
    )
  },

  /** 下载地址：直接用 <a>/location 触发，让浏览器处理 Content-Disposition。 */
  fileURL(fileId: string): string {
    return `${API_BASE}/files/${encodeURIComponent(fileId)}`
  },

  /** 分页预览地址：交给 iframe 加载，避免把 HTML 片段当字符串处理。 */
  previewURL(docId: string): string {
    return `${API_BASE}/documents/${encodeURIComponent(docId)}/preview`
  },

  /** 图片节点原始字节。 */
  imageURL(docId: string, nodeId: string): string {
    return `${API_BASE}/documents/${encodeURIComponent(docId)}/images/${encodeURIComponent(nodeId)}`
  },

  /** SSE 进度流地址。 */
  eventsURL(jobId: string): string {
    return `${API_BASE}/jobs/${encodeURIComponent(jobId)}/events`
  },
}

/** 触发浏览器下载而不离开当前页面。 */
export function triggerDownload(url: string, filename: string): void {
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
}

/** 人类可读的文件大小。 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`
}
