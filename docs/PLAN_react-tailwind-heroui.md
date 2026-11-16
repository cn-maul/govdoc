# GovDoc 前端迁移计划：Vue 3 → React 19 + Tailwind CSS v4 + HeroUI v3

## 1. 目标

1. 前端由 Vue 3 + 手写 CSS 迁移为 **React 19 + TypeScript + Tailwind CSS v4 + HeroUI v3**。
2. Go 后端最低版本提升到 **go 1.27.3**，`github.com/cn-maul/rosetta` 升级到 **v1.3.2**。
3. 功能、接口契约、交互行为与中文文案保持等价：不丢任何校对/批量修复/逐字比对能力。
4. 构建产物仍输出到 `web/dist`，继续由根目录 `embed.go` 的 `//go:embed all:web/dist` 内嵌。
5. 废止 `docs/DESIGN_heroUI.md` 中「Vue3 + 手写 CSS，禁止引入组件库」的旧约束，改为本文技术栈。

## 2. 不可变约束

- **接口契约不变**：`docs/CONTRACTS.md` 中的 `/api/*` 路径、请求/响应字段不变。
- **纯函数层不动**：`web/src/api/client.ts`、`web/src/api/types.ts`、`web/src/lib/{tree,edits,report,specEdit}.ts` 保持原样。
- **测试不丢**：`web/tests/specEdit.test.ts` 继续用 `node --test --experimental-strip-types` 通过。
- **原文逐字不变**：状态层不得修改 `source` 节点 `text`；只允许改方案结构。
- **构建路径不变**：`base: './'`、`outDir: 'dist'`、`assetsDir: 'assets'`、`target: 'es2020'` 保留。
- **开发代理不变**：`/api` → `http://127.0.0.1:8080`。
- 不引入路由库；仍用单页 + `tab` 状态切换。

## 3. 版本与依赖

### Go

```text
go 1.27.3
github.com/cn-maul/rosetta v1.3.2
```

### 前端

| 包 | 版本 | 用途 |
| --- | --- | --- |
| react / react-dom | ^19.3.1 | UI 运行时 |
| @types/react / @types/react-dom | latest | 类型 |
| vite | ^8.3.8 | 构建 |
| @vitejs/plugin-react | ^6.3.0 | React 支持 |
| tailwindcss | ^4.3.5 | 样式引擎 |
| @tailwindcss/vite | ^4.3.5 | Vite 集成 |
| @heroui/react | ^3.2.7 | 组件库 |
| @heroui/styles | ^3.2.7 | HeroUI 样式/主题 |
| tailwind-variants | ^3.3.1 | HeroUI 依赖 |
| react-aria / react-aria-components | HeroUI peer 依赖，由 npm 解析 | 可访问性 |
| typescript | ^5.9.0（保留） | 类型检查 |

删除：`vue`、`@vitejs/plugin-vue`、`vue-tsc`、`src/env.d.ts` 中的 `*.vue` 声明。

### 样式引入顺序（必须）

```css
@import "tailwindcss";
@import "@heroui/styles";
/* 项目令牌与少量结构性样式 */
@import "./styles/tokens.css";
@import "./styles/app.css";
```

不再使用 v2 的 `heroui()` Tailwind 插件，不需要 `framer-motion`，不需要 `HeroUIProvider`。

## 4. 迁移后目录

```text
web/
  index.html                 # <div id="root">，入口 /src/main.tsx
  vite.config.ts             # react() + tailwindcss()
  tsconfig.json              # jsx: react-jsx，include *.ts/*.tsx
  package.json
  src/
    main.tsx
    App.tsx
    api/                     # 原样保留
    lib/                     # 原样保留
    store/
      reactive.ts            # 极简深层 Proxy + computed 兼容层
      govdoc.ts              # 由 composables/useGovdoc.ts 迁移
    ui/
      primitives.tsx         # HeroUI 适配层（Btn/LinkButton/Select/CheckboxField/...）
    components/
      PanelUpload.tsx
      PanelAnalyze.tsx
      PanelReview.tsx        # 左预览 / 右校正两栏工作台
      PanelPreview.tsx
      PanelSettings.tsx
      PanelSettingsAI.tsx
      PanelSettingsProfile.tsx
      SpecEditor.tsx
      PreviewPane.tsx        # 可嵌入的 A4 纸张（工作台左栏与预览页共用）
    styles/
      index.css              # 按需引入 HeroUI 组件样式（见 DESIGN_heroUI §2）
      tokens.css             # 保留设计令牌（oklch），作为 HeroUI 主题覆盖
      app.css                # 仅保留纸张预览缩放等少量结构性样式
  tests/                     # 原样保留
```

删除：`web/src/**/*.vue`、`web/src/composables/useGovdoc.ts`、`web/src/styles/{base,components,layout}.css`、`web/src/main.ts`、`web/src/env.d.ts`。

## 5. 状态层设计

Vue 版本是模块级 `reactive` 单例 + 若干 `computed`。为降低迁移风险，保留这一形态：

- `store/reactive.ts`
  - `reactive<T>(obj)`：深层 `Proxy`，`set`/`deleteProperty` 时自增版本号并通知订阅者；嵌套对象用 `WeakMap` 缓存代理，保持引用稳定。
  - `computed<T>(fn)`：返回 `{ get value(): T }` 的惰性 getter 兼容对象。
- `store/govdoc.ts`
  - 逻辑与 `useGovdoc.ts` 一一对应，仅把 `import { computed, reactive } from 'vue'` 换成 `../store/reactive`。
  - 追加：
    - `subscribe(listener)` / `getVersion()`
    - `useGov()`：`useSyncExternalStore(subscribe, getVersion)`，React 组件在顶部调用一次以订阅变更。
  - 所有导出函数/常量名保持不变（`gov`、`bootstrap`、`startAnalysis`、`editPlan`、`hasDocument.value` 等）。
- 组件只读 `gov`，写入一律调用 store 导出函数（沿用原约定）。
- 组件本地状态用 `useState`；本地派生用 `useMemo`；生命周期/监听用 `useEffect`；Vue `watch` 改为 `useEffect` + 依赖数组。

## 6. 组件映射表

| Vue | React | 说明 |
| --- | --- | --- |
| `main.ts` | `main.tsx` | `createRoot(...).render(<App />)` |
| `App.vue` | `App.tsx` | 导航/页签、bootstrap、通知条、页脚 |
| `PanelUpload.vue` | `PanelUpload.tsx` | 上传、格式配置、profile 选择 |
| `PanelAnalyze.vue` | `PanelAnalyze.tsx` | SSE 阶段进度、诊断、原文预览入口 |
| `PanelReview.vue` | `PanelReview.tsx` | 最大块：漏收/重复/改动逐条接受拒绝、批量修复、方案树、成文比对 |
| `PanelPreview.vue` | `PanelPreview.tsx` | 分页预览与缩放 |
| `PanelSettings.vue` | `PanelSettings.tsx` | 设置壳 |
| `PanelSettingsAI.vue` | `PanelSettingsAI.tsx` | LLM 配置 |
| `PanelSettingsProfile.vue` | `PanelSettingsProfile.tsx` | 排版规则 profile 管理 |
| `SpecEditor.vue` | `SpecEditor.tsx` | 版式参数编辑 |

逐组件要求：

1. 保持所有按钮、禁用条件、提示文案、`title`、校验分支。
2. `v-model` → `value` + `onChange`/`onInput`；`v-model.number` → `Number(e.target.value)`。
3. `@click` → `onClick`；HeroUI 按钮优先用 `onPress`。
4. `v-if`/`v-else-if`/`v-else` → 条件表达式或早返回。
5. `v-for` → `map`，保留 `key`。
6. `<template v-for>` → `React.Fragment key=...`。
7. `:disabled` → `disabled`；`:title` → `title`；`:aria-*` → `aria-*`。
8. `defineProps` → 显式 `Props` 接口；`defineEmits` → 回调 props。
9. 不使用 `dangerouslySetInnerHTML`（原文全部按文本渲染）。

## 7. UI 与样式策略

### HeroUI 使用范围

优先使用：`Button`、`Input`、`Textarea`、`Select`、`Checkbox`、`Card`、`Alert`、`Chip`/`Badge`、`Tabs`、`Progress`、`Spinner`、`Accordion`/`Disclosure`、`Modal`（如有）。

- HeroUI v3 使用复合组件（如 `Card.Header` / `Card.Content`），不传扁平 props。
- 语义变体：`primary` / `secondary` / `tertiary` / `danger` / `outline` / `ghost`。
- 安装后以 `node_modules/@heroui/react` 的实际类型声明为准，禁止凭 v2 记忆写 API。

### Tailwind 使用范围

- 布局：`flex`、`grid`、`gap-*`、`space-y-*`、`max-w-*`、响应式断点（原 1100/900/640 对应 `lg`/`md`/`sm` 近似）。
- 排版与颜色：优先 Tailwind token 类（`text-foreground`、`bg-surface`、`border-divider` 等）；缺失时用 `text-[var(--...)]`。
- 保留 `tokens.css` 作为主题变量源，并让 HeroUI 的 `oklch` 变量覆盖/复用。
- `app.css` 只保留难以用工具类表达的结构：A4 纸张缩放、分页容器、树缩进辅助变量。
- 禁止继续新增 `.gd-*` 手写组件样式；原 `components.css`/`layout.css` 删除。

## 8. 执行阶段

### 阶段 0：基线

```powershell
git status
go version
node --version
pnpm --version
```

记录基线：`go build ./...`、`pnpm -C web test`、`pnpm -C web build` 通过。

### 阶段 1：Go 依赖升级

1. `go.mod`：`go 1.27` → `go 1.27.3`。
2. `go get github.com/cn-maul/rosetta@v1.3.2`。
3. `go mod tidy`。
4. 修复 `internal/llm/*` 编译差异（保持 `ErrNotConfigured`、`ErrTruncated`、`maxRepairRounds` 等语义）。
5. 验证：

```powershell
gofmt -l .
go vet ./...
go build ./...
go test ./...
```

### 阶段 2：前端工具链切换

1. 改 `web/package.json` 依赖与脚本：
   - `dev`: `vite`
   - `typecheck`: `tsc --noEmit`
   - `build`: `tsc --noEmit && vite build`
   - `build:only`: `vite build`
   - `test`: 保持 `node --test --experimental-strip-types tests/*.test.ts`
2. `pnpm -C web install`。
3. 改 `vite.config.ts`：`react()` + `tailwindcss()`，保留 `base/outDir/assetsDir/target/proxy`。
4. 改 `tsconfig.json`：`jsx: "react-jsx"`，`include` 加 `src/**/*.tsx`，移除 `.vue`。
5. `index.html`：`<div id="root">`、入口 `main.tsx`。
6. 新建 `src/main.tsx`，样式入口 `src/styles/index.css`。

验证：`pnpm -C web typecheck`（此时组件尚未迁移，允许失败，但配置错误必须清零）。

### 阶段 3：状态层迁移

1. 新建 `src/store/reactive.ts`。
2. 以 `useGovdoc.ts` 为蓝本生成 `src/store/govdoc.ts`，只替换导入与响应式原语。
3. 增加 `subscribe` / `getVersion` / `useGov`。
4. 用 `node --test` 与 `tsc` 校验纯函数与 store 类型。

### 阶段 4：UI 基础件与 App 壳

1. 新建 `src/ui/primitives.tsx`：统一 `SectionCard`、`Alert`、`Chip`、`Field`、`Row`、`EmptyState`、`Spinner` 等。
2. 迁移 `App.tsx`：导航、bootstrap、notice、页脚。
3. 迁移 `PanelSettings.tsx` 作为最简单样板。

验证：`pnpm -C web dev` 可启动，健康检查请求发出。

### 阶段 5：业务面板迁移

按依赖顺序：

1. `PanelUpload` → `PanelAnalyze` → `PanelPreview`。
2. `PanelSettingsAI`、`PanelSettingsProfile`、`SpecEditor`。
3. `PanelReview`（最后，工作量最大）。

每个组件完成后立即跑 `pnpm -C web typecheck`。

### 阶段 6：清理与验收

1. 删除全部 `.vue`、`useGovdoc.ts`、`main.ts`、`env.d.ts`、旧 CSS。
2. 更新 `docs/DESIGN_heroUI.md` 为 React 版规范；更新 `web/package.json` 描述。
3. 全量验证：

```powershell
gofmt -l .
go vet ./...
go build ./...
go test ./...
pnpm -C web typecheck
pnpm -C web test
pnpm -C web build
```

4. 端到端（按仓库现有工具）：

```powershell
go run ./tools/e2e
```

如 `tools/e2e` 需要参数或外部 LLM，则至少执行 `tools/stublm` + `tools/verify_*.go` 中不需要真实模型的用例，并在结果中说明。

5. 检查 `web/dist` 已重新生成、`go build ./...` 仍能内嵌。

## 9. 验收清单

- [ ] `go.mod` 为 `go 1.27.3`，rosetta 为 `v1.3.2`，`go mod tidy` 后无差异。
- [ ] `go build ./...`、`go test ./...` 通过。
- [ ] `web/package.json` 无 Vue 相关依赖。
- [ ] `tsc --noEmit` 通过。
- [ ] `pnpm test` 通过。
- [ ] `vite build` 通过，产物在 `web/dist`。
- [ ] 浏览器中四个工作流页签均可打开：上传 / 分析 / 校对 / 预览；设置页由右上角模型状态按钮进入。
- [ ] 上传、分析 SSE、方案编辑防抖校验、逐条接受拒绝、批量修复、成文下载、分页预览均可用。
- [ ] 兼容 1100/900/640 三档响应式布局。
- [ ] 无 `.vue` 文件、无 `vue` 依赖、无旧 `components.css`/`layout.css` 引用。

## 10. 风险与回滚

| 风险 | 缓解 |
| --- | --- |
| HeroUI v3 实际组件 API 与文档记忆不符 | 安装后先读 `node_modules/@heroui/react` 类型与 source；不确定的组件退回原生元素 + Tailwind |
| 深层 Proxy + `useSyncExternalStore` 出现 tearing | 只订阅版本号，读取同一模块级对象；必要时对热点列表加 `useMemo` |
| 迁移遗漏交互 | 以组件映射表逐项核对禁用条件、文案、分支 |
| Tailwind v4 与 HeroUI 样式顺序错误 | 固定 `tailwindcss` → `@heroui/styles` → 项目样式 |
| rosetta API 破坏性变更 | 先 `go build ./...` 暴露，按 `internal/llm` 单点修复 |
| 迁移失败 | 迁移前提交基线（或记录 `git rev-parse HEAD`），全部改动可 `git reset --hard` 回滚 |

## 11. 提交策略

- 不自动提交；全部完成后由用户确认再提交。
- 建议分阶段提交：Go 依赖 → 工具链 → 状态层 → 组件 → 清理文档。
