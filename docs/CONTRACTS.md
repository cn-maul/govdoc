# GovDoc 冻结接口契约 (v1)

> **本文件是并行开发的不变约定。** 任何工作流若要修改这里的一行，必须先通知 Lead；
> 未列入本文件的内部实现细节归各自工作流自由决定。

## 0. 全局约束

| 项 | 值 |
|---|---|
| Go | 1.27（`go.mod`: `go 1.27`, module `govdoc`） |
| 第三方依赖 | 仅 `github.com/cn-maul/rosetta v1.0.1`；其余用标准库 |
| 前端 | Vue 3 + TypeScript + **手写 CSS**（禁止 Tailwind/Element/组件库 CSS） |
| 前端产物 | `web/dist` → 由 `internal/server` 用 `//go:embed` 嵌入 |
| 格式标准 | 《党政机关公文格式》GB/T 9704-2012 |
| 核心不变式 | **改动必须显式声明、可见、可审计**：原文可以被修正，但每一处修正都必须是一条 `docmodel.Edit`（带 before/after/理由），未声明的改写仍会导致导出被拒 |

## 1. 数据契约（已完成，冻结）

`internal/docmodel` — 见 `model.go` / `verify.go`
`internal/gbstandard` — 见 `spec.go`
`internal/config` — 见 `config.go`

关键类型：`Document`/`Node`/`NodeKind`/`Run`、`Plan`/`Section`/`Meta`/`ElementBinding`/`Edit`、
`Doc`/`OutBlock`/`Role`、`Spec`/`ParaStyle`/`PageSetup`/`FontSize`、`Report`/`VerbatimDiff`。

### 1.1 逐字不变的三道防线（演变为「不得静默修改」）

早期版本承诺「原文一字不动」。用户需要工具修错别字与语病，因此规则改为更强、也更有用的一条：

> **原文可以被修改，但每一处修改都必须显式声明、可见、可审计。**

三道防线随之调整：

1. **只有 `internal/assemble` 改写正文**，且只通过 `docmodel.AppliedText` 施加**已声明的 `Plan.Edits`**；
   没有任何一处代码可以无条件改写文本。
2. `docmodel.VerifyPlan` 要求每个正文节点被**恰好引用一次**（丢失或重复都是 error）。
3. `docmodel.VerifyVerbatim` 拿「原文 + 该节点的已声明改动」作为期望值比对输出：
   - 差异若被声明 → 记入 `Diff.Changed`（供用户逐条审核），**不算失败**；
   - 差异若未声明 → 记入 `Diff.Altered`，**导出被拒**；
   - `Before` 在原文中找不到 → 记入 `Diff.Unapplied`（该条未生效，文档本身无误）。

因此「允许 AI 改字」与「不能偷偷改」同时成立：模型能提出的修改变多了，
但**可能静默发生**的修改仍然是零。

### 1.2 三种认领方式（合起来必须覆盖全部正文节点）

- `Sections` —— 章节树（`heading_node_id` + `body_node_ids`）
- `Elements` —— 公文要素绑定（标题/主送机关/署名/成文日期/抄送…），role 受 `BindableRoles()` 白名单约束
- `PreambleNodeIDs` —— 主送机关之下、第一个「一、」之前的缘由段（不是章节，**不得编号**）

### 1.3 改动声明（`Plan.Edits`）

```go
type EditKind string  // typo | grammar | wording | punctuation | sentence_split | heading | other
func AllEditKinds() []EditKind
func EditLabel(k EditKind) string

type Edit struct {
    NodeID     string   `json:"node_id,omitempty"` // 必填：改动发生在某一节点内部
    Kind       EditKind `json:"kind"`
    Before     string   `json:"before"`       // 必须与原文逐字一致
    After      string   `json:"after"`
    Reason     string   `json:"reason,omitempty"`
    Occurrence int      `json:"occurrence,omitempty"` // Before 多次出现时选第几处
}
```

**`Edit` 是在一个节点内部替换文字**，因此 `NodeID` 必填。`node_id` 为空的改动**无法应用**，
会被记入 `VerbatimDiff.Unapplied` 并在原因里说明正确做法——它绝不会被静默丢弃。

#### 生成段标题走结构，不走 edits

「原文缺少标题」是**结构**问题，不是文字替换，用章节表达：

```go
Section{ ID: "s1", Title: "一、总体要求", Level: 1,
         HeadingNodeID: "",                 // 留空 = 由 title 生成标题
         BodyNodeIDs: []string{"n0004"} }
```

装配器会生成一个标题段落，并计入 `Report.SynthesizedHeadings` 与 `Result.SynthText`，
因此用户可以复核「这个标题是模型加的」。模型提示词里已明确这一点，
`heading` **不在** edits 的类型清单中。

`KindTable` 的 `Node.Text` **故意留空**（载荷在 `Rows`）：为表格编造一段展平文本本身
就是伪造，解析器拒绝这么做是正确的。校验器已按 kind 分别记账。

**不要修改这些包**。需要新增字段时向 Lead 提出。

## 2. 各工作流对外函数签名（冻结）

```go
// internal/parse —— 上传文档 → 源文档模型
// 支持 docx / txt / md；不识别时返回 error。
// 必须: 按源顺序填充 Node.Order，为每个节点生成 "n0001" 形式 ID，
//       Kind 判定正确，Text 逐字取自源（不得 trim 掉正文内部空白）。
func Parse(name string, data []byte) (*docmodel.Document, error)

// internal/llm —— 通过 rosetta 调用 LLM 产出结构方案
type Config struct {
    Endpoint string        // 如 https://api.deepseek.com/v1
    APIKey   string
    Model    string        // 如 deepseek-chat
    Protocol string        // "" | "openai-chat" | "openai-responses" | "anthropic"
    Timeout  time.Duration // 0 → 120s
}
type Stage struct {
    Name    string  `json:"name"`    // connect/analyze/repair/validate/complete
    Message string  `json:"message"` // 面向用户的中文描述
    Done    bool    `json:"done"`
}
func New(cfg Config) (*Client, error)
// Plan 返回方案并做最多 2 轮自修复。progress 可为 nil。
// 返回的 Plan 已通过 VerifyPlan；服务端仍会再独立校验一次（单一裁决点）。
func (c *Client) Plan(ctx context.Context, doc *docmodel.Document, progress func(Stage)) (*docmodel.Plan, error)
// PlanWithHint 同上，额外接受用户对文档性质的提示（提示不进入正文）。
func (c *Client) PlanWithHint(ctx context.Context, doc *docmodel.Document, hint string, progress func(Stage)) (*docmodel.Plan, error)
// ParsePlan 是纯函数，便于对模型输出的各种畸形做单元测试。
func ParsePlan(text string, doc *docmodel.Document) (*docmodel.Plan, error)

// internal/assemble —— 方案 → 渲染文档（计算层次序数编号、版头/版记排布）
// 失败时返回 *Result（含 Report）与错误，便于前端展示原因。
type Result struct {
    Doc       *docmodel.Doc
    SynthText []string               // 全部非原文文字，供人工审计
    Diff      *docmodel.VerbatimDiff
    Report    *docmodel.Report
}
func Build(doc *docmodel.Document, plan *docmodel.Plan, spec gbstandard.Spec) (*Result, error)

// internal/render/docx —— 渲染文档 → .docx 字节
func Render(d *docmodel.Doc, spec gbstandard.Spec) ([]byte, error)

// internal/render/preview —— 渲染文档 → HTML 片段（用于分页预览）
func Render(d *docmodel.Doc, spec gbstandard.Spec) ([]byte, error)
```

`internal/llm` **只负责调用与解析 JSON**；最终裁决在服务端（`docmodel.VerifyPlan`）。
`internal/render/*` **不得硬编码字号/行距**，一律读 `gbstandard.Spec`。

## 3. HTTP API（冻结；前后端共同实现）

统一前缀 `/api`。错误响应恒为 `{"error":{"code":"...","message":"..."}}`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | `{status,version,llm_configured}` |
| GET | `/api/config` | 当前配置（**api_key 永不回传**，只回 `api_key_set: bool`） |
| PUT | `/api/config` | 更新 LLM 配置与行文方向 |
| GET | `/api/spec` | 当前 `gbstandard.Spec` + `fonts[]` 可用性 |
| GET | `/api/profiles` | `{profiles: Profile[], active: string}` |
| POST | `/api/profiles` | 新建（`copy_from` 表示从某套复制规则）→ `Profile` |
| POST | `/api/profiles/activate` | `{id}` 设为新文档的默认 → `ProfilesResponse` |
| PUT | `/api/profiles/{id}` | 更新名称/说明/规则 → `Profile`；内置返回 400 |
| DELETE | `/api/profiles/{id}` | 删除 → 204；内置/默认/仍被文档使用分别返回 400/400/409 |
| POST | `/api/documents` | multipart 上传，字段 `file`（可选 `profile`）→ `{doc_id, source_name, format, node_count, profile_id, nodes[]}`。`profile_id` 回显**实际生效**的配置（未知值会回退到公文） |
| GET | `/api/documents/{id}` | 源 `docmodel.Document` |
| GET | `/api/documents/{id}/images/{node_id}` | 图片节点原始字节 |
| POST | `/api/documents/{id}/plan` | 启动 LLM 分析 → `{job_id}` |
| GET | `/api/jobs/{job_id}/events` | **SSE** 进度：`event: stage` / `event: done` / `event: error` |
| GET | `/api/documents/{id}/plan` | `{plan, report}` |
| PUT | `/api/documents/{id}/plan` | 提交用户编辑后的 plan → 重新校验 → `{plan, report}` |
| POST | `/api/documents/{id}/render` | `{format:"docx"}` → `{file_id, filename, report, diff}` |
| GET | `/api/files/{file_id}` | 下载（`Content-Disposition: attachment`） |
| GET | `/api/documents/{id}/preview` | `text/html` 分页预览 |

SSE 帧格式固定：

```
event: stage
data: {"name":"analyze","message":"正在分析文档结构…","done":false}

event: done
data: {"plan":{...},"report":{...}}
```

## 4. 目录与写权限（互斥，避免冲突）

| 路径 | 归属 |
|---|---|
| `internal/docmodel/**`, `internal/gbstandard/**`, `internal/config/**` | **Lead** |
| `internal/api/**`, `internal/assemble/**`, `internal/server/**`, `cmd/**` | **Lead** |
| `internal/parse/**` | parser-dev |
| `internal/llm/**` | Lead（原 llm-dev 中断后由 Lead 完成） |
| `internal/render/docx/**` | docx-dev |
| `internal/render/preview/**`, `web/**` | web-dev / web-dev-2 |
| `testdata/**` | 各自按需新建子目录，勿动他人文件 |

## 5. 验收口径

> **构建前必须 `GOWORK=off`**（`bash tools/build.sh` 已内置）。
> 本目录的上一级有一个属于另一个项目（rosetta）的 `go.work`，其中不包含本模块，
> 于是 Go 会直接拒绝解析 `./...`：
>
> ```
> pattern ./...: directory prefix . does not contain modules listed in go.work
> or their selected dependencies
> ```
>
> 这个报错看起来像"代码坏了"，实际是工作区成员问题，最容易被误判。

1. `go build ./...` 与 `go vet ./...` 零输出，`gofmt -l` 无输出。
2. `go test ./...` 全绿。
3. `VerifyPlan`：丢失/重复/引用不存在节点/未知要素 role → 必须报 error。
4. `VerifyVerbatim`：
   - 已声明的改动 → `Clean()==true`，且出现在 `Diff.Changed`；
   - **未声明的改写** → `Clean()==false`（导出被拒）；
   - `Before` 找不到 → 出现在 `Diff.Unapplied`，文档仍可导出。
5. 生成的 .docx 用 python-docx 读回：页边距 = 28/26/37/35mm，正文三号(16pt)仿宋，
   固定行距 ≈28.99pt，首行缩进 2 字符，标题二号小标宋居中。
6. 单二进制 `go build ./cmd/govdoc` 可正常启动并提供内嵌界面。
7. 配置持久化：`PUT /api/config` 后**重启进程**，`GET /api/config` 仍返回已保存的值，
   且 `health.llm_configured == true`（模型客户端也拿到保存的配置）。
   设置文件位于 **exe 同目录**的 `govdoc.config.json`。
8. **配置之间互不覆盖**：保存 AI 设置不得清空 profile 列表，编辑 profile 不得清空 API Key。
   两者共用一条读-改-写路径（`Server.persist`）。
9. 文档记住所用 profile：切换全局默认后，已上传文档仍按原 profile 渲染。
10. 套用不同 profile 导出的 .docx 必须**实测不同**（`bash tools/verify_profiles_http.sh`
    用 python-docx 读回正文字体/字号/页边距逐项比对）。
