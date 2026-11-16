# GovDoc 前端技术规范 —— React 19 + Tailwind CSS v4 + HeroUI v3

> 迁移计划与验收口径见 [PLAN_react-tailwind-heroui.md](PLAN_react-tailwind-heroui.md)。
> 本文是**迁移后**的规范；旧版「Vue 3 + 手写 CSS，禁止引入组件库」的约束已废止。

## 1. 技术栈与硬约束

| 层 | 选型 |
| --- | --- |
| UI 运行时 | React 19（`react` / `react-dom`） |
| 语言 | TypeScript（`strict`、`noUnusedLocals`、`noUnusedParameters`） |
| 样式引擎 | Tailwind CSS v4（`@tailwindcss/vite`，无 `tailwind.config.js`） |
| 组件库 | HeroUI v3（`@heroui/react` + `@heroui/styles`） |
| 构建 | Vite（`base: './'`、`outDir: 'dist'`、`assetsDir: 'assets'`、`target: 'es2020'`） |

不可变约束：

- **设计令牌保留在 `web/src/styles/tokens.css`**，仍是 HeroUI v3 默认主题的逐字复制（oklch +
  `color-mix`），不要换算成 hex，否则 hover/soft 派生色会产生可见偏差。
- 不引入路由库：仍是单页 + `tab` 状态切换。
- 不使用 `dangerouslySetInnerHTML`：原文一律按文本渲染。
- 构建产物落在 `web/dist`，由根目录 `embed.go` 内嵌。

## 2. 样式引入顺序（必须）

`web/src/styles/index.css` **按需引入 HeroUI 组件样式**，不写 `@import "@heroui/styles"`
（那是全量 86 个组件，产物 734 kB；按需约 330 kB）。顺序与分层都有意义：

```css
@layer theme, base, components, utilities;

@import "tailwindcss";
@import "@heroui/styles/base" layer(base);
@import "@heroui/styles/base/scrollbar.css" layer(base);
@import "@heroui/styles/themes/default" layer(theme);

/* 共享件在前 */
@import "@heroui/styles/components/label.css" layer(components);
/* …按字母/用途逐个组件… */

@import "@heroui/styles/utilities";
@import "@heroui/styles/variants";

@import "./tokens.css";
@import "./app.css";
```

三条必须守住的规则：

1. **`@layer theme, base, components, utilities;` 必须保留，且每个组件 import 都要带
   `layer(components)`。** 少了分层，HeroUI 的组件样式会盖住调用点写的 Tailwind 工具类，
   连 `text-[13px]` 这种最基本的覆盖都会失效（表格、卡片标题会整体变大一号）。
   验证方式：同一页面在「全量 CSS」与「按需 CSS」两版下逐个 `[data-slot]` 采样
   `getComputedStyle`，结果必须完全一致。
2. **共享件必须排在组件之前**（`label` / `description` / `field-error` / `close-button`）。
   组件靠后面的规则覆盖共享件的默认值。
3. **新用到 HeroUI 组件时，要在 `index.css` 补一行 CSS**，否则组件结构对了但没有样式；
   改完跑一遍逐页截图核对。

- `tokens.css` 提供 `--background` / `--surface` / `--accent` / `--danger` 等语义变量；
  HeroUI 组件读的是同一套变量名，因此主题天然一致。
- `app.css` 只放**工具类难以表达**的结构：A4 纸张缩放（`.gd-paper-stage` / `.gd-paper-scaler` /
  `.gd-paper-frame`）、`prefers-reduced-motion`、少量文本辅助类。

## 3. 组件使用规范

优先使用 HeroUI v3 的复合组件（`Card.Header` / `Card.Content`、`Alert.Title` / `Alert.Description`、
`Table.Root` 等），不要凭 v2 记忆写扁平 props —— **以 `node_modules/@heroui/react` 的实际类型声明为准**。

本项目常用入口：

| 用途 | 组件 |
| --- | --- |
| 按钮 | `Button`（`variant`: `primary` / `secondary` / `tertiary` / `danger` / `danger-soft` / `outline` / `ghost`；`size`: `sm` / `md` / `lg`） |
| 单选题组（筛选、缩放、默认规则） | `ToggleButtonGroup`（`selectionMode="single"` + `selectedKeys` / `onSelectionChange`）+ `ToggleButton` |
| 页内分栏与导航 | `Tabs` + `Tabs.List` / `Tabs.Tab` / `Tabs.Panel`（支持 `orientation="vertical"`） |
| 需要保留未提交表单的页签 | `Tabs.Panel shouldForceMount` + `className="data-[inert=true]:hidden"`：RAC 默认卸载未选中面板，会清空用户填了一半的输入；force mount 后要自己按 `data-inert` 隐藏，否则两页同时显示 |
| 折叠块 | `Disclosure` + `Disclosure.Heading` / `Trigger` / `Content` / `Body` |
| 下拉选择 | `Select` 复合件（`Trigger` / `Value` / `Indicator` / `Popover` + `ListBox`） |
| 表格 | `Table` + `Table.ScrollContainer` / `Content` / `Header` / `Column` / `Body` / `Row` / `Cell` |
| 卡片 / 面板 | `Card` + `Card.Header` / `Card.Title` / `Card.Description` / `Card.Content` / `Card.Footer` |
| 提示条 | `Alert` + `Alert.Indicator` / `Alert.Content` / `Alert.Title` / `Alert.Description` |
| 轻提示 | `Toast.Provider` + 命令式 `toast(text, { variant, timeout })` |
| 徽标 | `Chip`（`color`: `default` / `accent` / `success` / `warning` / `danger`） |
| 复选框 | `Checkbox` + `Checkbox.Content` / `Control` / `Indicator`，说明用 `Description` |
| 加载 | `Spinner`、`ProgressBar`（`isIndeterminate`） |

通用展示件统一放在 `web/src/ui/primitives.tsx`（见 §3.3），业务组件只从那里取，
不在各处重复拼装。

**不要手写「看起来像」的控件。** 一排 `aria-pressed` 的按钮不是单选组，读屏器只会念
「按钮」；手写折叠箭头不带 `aria-expanded`；自己定位的浮层没有焦点陷阱与方向键。
凡是组件库有对应件，就用组件库的 —— 这不只是省代码，而是键盘、读屏与触屏行为本来就
不属于本项目的实现范围。

### 3.1 `Btn`：给 HeroUI Button 加 `title` 的包装

HeroUI 的 `Button` 建立在 `react-aria-components` 之上，后者用 `filterDOMProps` 把原生 `title`
过滤掉了（只放行 `id` / `aria-*` / `data-*` / 全局事件），所以：

- 直接写 `title="…"` **既过不了类型检查，也不会出现在 DOM 上**；
- 需要提示文案时用 `primitives.tsx` 里的 `Btn`：

```tsx
<Btn size="sm" variant="ghost" title="上移" isDisabled={i === 0} onPress={...}>↑</Btn>
```

`Btn` 用一个 `inline-flex` 的 `span` 承接 `title`，其余 props 原样透传。**原生** `<button>` /
`<span>` / `<option>` 上的 `title` 不受此限制，照常使用。

### 3.3 适配层 `web/src/ui/primitives.tsx`

业务组件只从这里取展示件。这一层存在的理由不是「再包一层」，而是 HeroUI v3 自己不便
表达的四件事：

| 导出 | 为什么需要 |
| --- | --- |
| `Btn` | RAC 的 `filterDOMProps` 会丢掉原生 `title`，而本项目大量按钮靠 title 说明「为什么禁用」。见 §3.1。 |
| `LinkButton` | 下载 / 新窗口必须是真 `<a>`（中键新标签、右键另存、悬停显 URL），但视觉要与 `Button` 一致。复用 HeroUI 的 `.button` + `.button--{size}` + `.button--{variant}` 类，而不是手写一套 rounded/bg/hover。 |
| `Select` | 把 `Trigger` / `Popover` / `ListBox` 三件套收成 `value` + `onChange` + `items`；空串显示 placeholder。注意 HeroUI Select 无法用空 key 表示「未选」，需要哨兵值（如设置页的 `AUTO_PROTOCOL = '__auto__'`）。 |
| `CheckboxField` | 把标签、控件与说明按「说明在 Content 之外」的正确结构摆好，避免说明被读成标签的一部分。 |
| `IndeterminateProgress` | 不确定进度条，自带 `role="progressbar"` 与 `aria-busy`。 |

其余（`SectionCard` / `AlertBox` / `Badge` / `Field` / `Row` / `Stack` / `EmptyState` /
`InlineSpinner` / `TextInput` / `TextArea`）都是薄封装，只补默认样式与中文排版间距。

### 3.2 交互事件

- 优先 `onPress`（react-aria 的语义事件，自带键盘/触摸处理）；`onClick` 仅在需要原生事件对象时使用。
- `v-model.number` 类输入用 `Number(e.target.value)`；其余 `onChange` 直接取 `e.target.value`。
- 禁用用 `isDisabled`（HeroUI / RAC 组件）或 `disabled`（原生元素），不要混用。

### 3.4 校对工作台：左预览 / 右校正

`PanelReview` 是三栏之外的两栏结构（左侧全局导航仍在外层，此处指主内容区）：

```
grid xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]
├── 左（sticky top-20）：<PreviewPane compact height="clamp(24rem,62vh,46rem)" />
└── 右：<Tabs defaultSelectedKey="edits">
        AI 改动 / 结构方案 / 原文节点 / 成文核对
```

- **左边必须是成品本身**，不是第二份摘要。用户校对时判断的是「这句话改完长什么样」，
  如果左栏只是文字列表，他仍然得切到预览页去验证，改一次来回一次。
- `PreviewPane` 同时被全屏预览页复用 —— 两处看到的必须是同一张纸。
- 左侧 `xl:sticky`：右栏滚到第 20 条结构变动时，纸还在眼前。
- 紧凑模式默认缩放 50%：左栏宽约 470px，A4 纸固定 794px，100% 时一张纸要横向滚动才能
  看全；缩放组仍可点，需要细看时放大即可。
- 右栏四个 Tab 的分工：`AI 改动` = 文字层面的声明改动 + 结构变动 + 手工补一条；
  `结构方案` = 章节树与逐节点归属；`原文节点` = 每段原文被谁引用、有没有漏收重复；
  `成文核对` = 逐字比对结果。它们回答的是四个不同的问题，不要合并。

## 4. Tailwind 使用规范

- **布局**：`flex` / `grid` / `gap-*` / `space-y-*` / `max-w-*`；响应式用 `sm`(40rem) / `md`(48rem) /
  `lg`(64rem) / `xl`(80rem)，对应旧版 640 / 900 / 1100 三档的近似值。
- **颜色**：优先 Tailwind 语义类（`text-foreground`、`bg-surface`、`bg-surface-secondary`、
  `border-border`、`text-muted`、`text-danger`、`text-success`、`bg-accent-soft` 等）；
  令牌里没有的用 `text-[var(--xxx)]` 逃生。
- **禁止**新增 `.gd-*` 手写组件样式；`components.css` / `layout.css` 已删除。
- 字体族与正文基础样式在 `app.css` 的 `body` 上一次性设定，不再逐组件声明。

## 5. 焦点环、禁用态与动效

```css
:focus-visible { outline: 2px solid var(--focus); outline-offset: var(--ring-offset-width); }
[disabled], [aria-disabled="true"] { opacity: var(--disabled-opacity); cursor: var(--cursor-disabled); }
```

动效只用令牌里的缓动（`--ease-smooth`、`--ease-out-fluid` 等），并保留
`@media (prefers-reduced-motion: reduce)` 的全局降级（见 `app.css`）。

## 6. 状态层

- `web/src/store/reactive.ts`：极简深层 `Proxy` + `computed` 兼容层（`get`/`set` 自增版本号）。
- `web/src/store/govdoc.ts`：模块级单例 `gov`，全部写操作都是导出函数。
- `web/src/store/useGov.ts`：`useSyncExternalStore(subscribe, getVersion)`；**组件在函数体顶部调用一次**即可订阅。
- 组件只读 `gov`，写入一律调用 store 导出函数；组件本地状态用 `useState`，本地派生用 `useMemo`，
  副作用用 `useEffect`（Vue 的 `watch` 改为 `useEffect` + 依赖数组）。

## 7. 与公文预览的关系（不要混淆）

这里再补一条实现注意：纸张的占位盒按**像素**算（794×1123 × 缩放），不要写成百分比。
`.gd-paper-scaler` 用 `width: ${794*zoom}px`，容器居中靠 `margin: auto` 而不是
`justify-content: center` —— 后者在内容比容器宽时会把溢出的那一半推到负坐标，滚动条到不了，
150% 缩放时纸张左半边就永远看不见。

**应用 UI 用 HeroUI 令牌；公文纸张预览区必须用 `gbstandard.Spec` 的数值**（A4 210×297mm、
页边距 28/26/37/35mm、正文三号 16pt 仿宋、固定行距 28.99pt）。两者不能混用：
预览区是「纸」，要严格还原国标；应用外壳是「软件界面」，用 HeroUI 语言。
预览区应当视觉上明确是「一张纸」（白底、投影、四周留白灰底衬托）。

## 8. 交付要求

- 目录结构见 `PLAN_react-tailwind-heroui.md` §4；`src/**/*.vue`、`composables/`、`main.ts`、
  `env.d.ts`、`styles/{base,components,layout}.css` 均已删除，不要复活。
- `pnpm typecheck`、`pnpm test`、`pnpm build` 三者必须全绿。
- 深色主题：令牌已备好，可通过 `[data-theme="dark"]` 切换（加分项，非必需）。
