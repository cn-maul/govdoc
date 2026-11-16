# GovDoc 冻结接口契约 (v1)

> **本文件是并行开发的不变约定。** 任何工作流若要修改这里的一行，必须先通知 Lead；
> 未列入本文件的内部实现细节归各自工作流自由决定。

## 0. 全局约束

| 项 | 值 |
|---|---|
| Go | 1.27.3（`go.mod`: `go 1.27.3`, module `govdoc`） |
| 第三方依赖 | 仅 `github.com/cn-maul/rosetta v1.3.2`；其余用标准库 |
| 前端 | React 19 + TypeScript + Tailwind CSS v4 + HeroUI v3（见 `docs/DESIGN_heroUI.md`） |
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

### 1.2 认领方式（合起来必须覆盖全部正文节点）

- `Sections` —— 章节树（`heading_node_id` + `body_node_ids` + `body_parts`）
- `Elements` —— 公文要素绑定（标题/主送机关/署名/成文日期/抄送…），role 受 `BindableRoles()` 白名单约束
- `PreambleNodeIDs` —— 主送机关之下、第一个「一、」之前的缘由段（不是章节，**不得编号**）

#### 整段重组（`Plan.Recompose` + `Section.BodyParts`）

一份「整篇一段到底」、没有任何层次证据的文档，可以重建章节；但一个原文节点只能
作为一个整体被引用一次，无法直接表达「拆成多段、放进多个章节」。为此：

```go
type Recompose struct {
    NodeID string   `json:"node_id"`
    Parts  []string `json:"parts"`   // 改写后的每个段落，按输出顺序
    Roles  []string `json:"roles,omitempty"` // 与 Parts 等长：recipient / preamble / ""
    Reason string   `json:"reason,omitempty"`
}
type BodyPart struct {
    NodeID string `json:"node_id"`
    Part   int    `json:"part"`      // parts 的下标，0 基
}
```

规则：

- 声明重组的节点**不得**再整体出现在 `body_node_ids`、`preamble_node_ids`、
  `elements` 或 `heading_node_id` 中；
- `roles` 只允许 `recipient`（主送机关）和 `preamble`（正文前言）。它们必须
  位于 `parts` 开头，先 `recipient` 后 `preamble`，由装配器直接放进骨架，
  **不得**再出现在章节的 `body_parts` 里；
- 其余普通正文分片必须在某个章节的 `body_parts` 里**恰好出现一次**；
- 同一节点的重组声明只能有一条，不能与同一节点的 `Edit` 并存（重组后的文字
  直接写进 `parts`）；
- 表格、图片不能重组；`parts` 中不得有空段；
- 重组后字数低于原文约 60% 时产生 `heavy_recompose` 警告，提示可能是在概括而非整理。

用户在校对区逐条「同意/不同意」：同意才把 `recompose` 与 `body_parts` 一起写回方案，
不同意则保留原文。重组的改动在导出日志中记为 `kind = "recompose"`，标签为「重组段落」。

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
    MaxOutputTokens int    // 0 → 8192 + 40×正文节点数（上限 32768）
    Temperature     *float64 // nil → 0.2（低于服务商默认值，保证同一文档多次分析稳定）
}
type Stage struct {
    Name    string  `json:"name"`    // connect/analyze/repair/validate/complete
    Message string  `json:"message"` // 面向用户的中文描述
    Done    bool    `json:"done"`
}
func New(cfg Config) (*Client, error)
// Plan 返回方案并做最多 2 轮自修复。progress 可为 nil。
// 返回的 Plan 已通过 VerifyPlan；服务端仍会再独立校验一次（单一裁决点）。
//
// 请求侧：优先用 response_format/json_schema 约束输出（各协议由 rosetta 映射）；
// 服务商拒绝该字段时自动退回纯 JSON，并在本次进程内记住，不再重复试探。
// 校验侧：先跑 internal/baseline.Repair 做确定性修复（漏收/重复认领/标题悬空），
// 修好了就不再消耗模型轮次；只有修不好的缺陷才带着具体清单回到模型。
func (c *Client) Plan(ctx context.Context, doc *docmodel.Document, progress func(Stage)) (*docmodel.Plan, error)
// PlanWithHint 同上，额外接受用户对文档性质的提示（提示不进入正文）。
func (c *Client) PlanWithHint(ctx context.Context, doc *docmodel.Document, hint string, progress func(Stage)) (*docmodel.Plan, error)
// ParsePlan 是纯函数，便于对模型输出的各种畸形做单元测试。
func ParsePlan(text string, doc *docmodel.Document) (*docmodel.Plan, error)

// internal/baseline —— 纯函数，不调用模型，三层用途共用一份原文结构
//  1. Build：按原文自带的层次序数切章节，产出「确定性下限」方案；
//  2. Repair：在调用模型前修正方案里可判定的结构错误（漏收/重复/标题悬空），
//     多数缺陷因此不必再消耗一轮模型；
//  3. Compare：方案与 Build 的差异清单，即接口里的 structure 字段。
func Build(doc *docmodel.Document) *docmodel.Plan
func Repair(doc *docmodel.Document, plan *docmodel.Plan) (*docmodel.Plan, []string)
func Compare(doc *docmodel.Document, base, plan *docmodel.Plan) *StructureDiff

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
| POST | `/api/llm/models` | 探测模型目录 → `{models: string[]}`。请求 `{endpoint, protocol?, api_key?}`；Key 为空时仅在接口地址与已保存值相同时复用已保存的 Key，否则 400 |
| GET | `/api/spec` | 当前 `gbstandard.Spec` + `fonts[]` 可用性 |
| GET | `/api/profiles` | `{profiles: Profile[], active: string}` |
| POST | `/api/profiles` | 新建（`copy_from` 表示从某套复制规则）→ `Profile` |
| POST | `/api/profiles/activate` | `{id}` 设为新文档的默认 → `ProfilesResponse` |
| PUT | `/api/profiles/{id}` | 更新名称/说明/规则 → `Profile`；内置返回 400 |
| DELETE | `/api/profiles/{id}` | 删除 → 204；内置/默认/仍被文档使用分别返回 400/400/409 |
| POST | `/api/documents` | multipart 上传，字段 `file`（可选 `profile`）→ `{doc_id, source_name, format, node_count, profile_id, nodes[]}`。`profile_id` 回显**实际生效**的配置（未知值会回退到公文） |
| GET | `/api/documents/{id}` | 源 `docmodel.Document` |
| GET | `/api/documents/{id}/images/{node_id}` | 图片节点原始字节 |
| POST | `/api/documents/{id}/plan` | 启动 LLM 分析 → `{job_id}`。可选 `{"baseline":true}`：**不调用模型**，直接按原文自带的层次序数生成方案，作业立即完成 |
| GET | `/api/jobs/{job_id}/events` | **SSE** 进度：`event: stage` / `event: partial` / `event: done` / `event: error` |
| GET | `/api/documents/{id}/plan` | `{plan, report, structure}` |
| PUT | `/api/documents/{id}/plan` | 提交用户编辑后的 plan → 重新校验 → `{plan, report, structure}` |
| POST | `/api/documents/{id}/render` | `{format:"docx"}` → `{file_id, filename, report, diff}` |
| GET | `/api/files/{file_id}` | 下载（`Content-Disposition: attachment`） |
| GET | `/api/documents/{id}/preview` | `text/html` 分页预览 |

SSE 帧格式固定：

```
event: stage
data: {"name":"analyze","message":"正在分析文档结构…","done":false}

event: partial
data: {"plan":{"sections":[{"id":"s1","title":"一、总体要求","level":1}]},"edits":[{"node_id":"n0001","kind":"punctuation","before":"，","after":"。"}]}

event: done
data: {"plan":{...},"report":{...},"structure":{...}}
```

`partial` 是**生成中的临时快照**：只包含已经闭合的章节/校对对象，可能缺少段落、
没有通过校验，前端只能用它先显示结构和校对项，不能用于预览、导出或写回方案。
新一轮模型调用开始时会先收到 `{"reset":true}`，客户端应清掉上一轮临时行。
终态仍以 `done` 帧里的方案为准。

`structure` 是**结构变动清单**：方案相对「原文自身结构」的差异，由服务端每次校验时
重新推导（基线不依赖模型，因此永远可用）。字段为
`{moves:[{node_id,text,kind,from,from_id,to,to_id}], sections:[{kind,id,title,baseline_title?}], moved_sections:[{id,title,baseline_pos,rendered_pos}]}`，
三者恒为数组（无差异时为空数组，不是 `null`）。`report` 只回答「有没有丢、有没有重复」，
`structure` 回答「哪一段换了章节、哪个章节增删改名或换了位置」——两者互补。

## 4. 目录与写权限（互斥，避免冲突）

| 路径 | 归属 |
|---|---|
| `internal/docmodel/**`, `internal/gbstandard/**`, `internal/config/**` | **Lead** |
| `internal/api/**`, `internal/assemble/**`, `internal/server/**`, `cmd/**` | **Lead** |
| `internal/parse/**` | parser-dev |
| `internal/llm/**` | Lead（原 llm-dev 中断后由 Lead 完成） |
| `internal/baseline/**` | Lead（新增：确定性基线、本地预修复、结构差异） |
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
