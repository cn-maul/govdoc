# GovDoc 前端视觉规范 —— 对齐 HeroUI v3 设计语言

> 来源：本地参考实现 `C:\Users\louis\Desktop\project\heroui-3.2.6`
> （`packages/styles/themes/default/variables.css`、`components/{button,card,input,typography}.css`、
> `themes/shared/theme.css`、`base/base.css`）。
>
> **重要**：HeroUI 本身是 Tailwind v4 组件库，本项目约束是「Vue3 + 自实现 CSS，禁止组件库」。
> 因此这里做的是**用原生手写 CSS 复刻 HeroUI 的视觉语言**：同一套设计令牌、同样的造型、
> 同样的动效曲线与阴影层次。**不要把 HeroUI 或 Tailwind 装进项目。**

## 1. 令牌（tokens.css）—— 直接复制自 HeroUI，勿自行改数值

完整的可直接使用文件见 `docs/design-tokens.css`。把它复制为 `web/src/styles/tokens.css`。
关键点：

- HeroUI 用 **oklch** 定义颜色、用 **color-mix(in oklab/oklch)** 派生 hover/soft 色。
  现代浏览器（Chrome 111+/Safari 15.4+/Firefox 113+）均支持，**保持一致即可，不要换算成 hex**，
  否则 hover 派生色会与参考实现产生可见偏差。
- 圆角体系由单一 `--radius: 0.5rem` 推导：
  `--radius-xs .25× --radius-sm .5× --radius-md .75× --radius-lg 1× --radius-xl 1.5× --radius-2xl 2× --radius-3xl 3×`。
- 表单控件圆角是 **`--radius-field: calc(var(--radius) * 1.5)` = 12px**。
- 卡片圆角是 **`min(32px, var(--radius-3xl))` = 24px**。
- 按钮圆角是 **`rounded-3xl` = 24px**，高度 40px（`md` 36px）→ 呈**胶囊形**。这是 HeroUI v3 的显著特征。
- 输入框**默认无边框**（`--field-border-width: 0px`，`--field-border: transparent`），靠
  底色 `--field-background` + `--field-shadow` 与背景区分，focus 时才出现 ring。这是最容易做错的地方。

## 2. 组件配方（复刻自 HeroUI 实际 CSS）

### 按钮 `.gd-btn`
```
h-10 / md:h-9 (2.5rem / 2.25rem)   px-4   gap-2
rounded: 24px (胶囊)   font-size 14px / font-weight 500   white-space nowrap
transition: transform 250ms var(--ease-smooth),
            background-color 100ms var(--ease-out),
            box-shadow 100ms var(--ease-out)
:hover  → background-color: var(--button-bg-hover)
:active → background-color: var(--button-bg-pressed); transform: scale(0.97)
:focus-visible → ring (见 §3)
:disabled → opacity 0.5; cursor not-allowed
```
变体（只改 CSS 变量，与 HeroUI 一致）：
| 变体 | bg | hover | fg |
|---|---|---|---|
| primary | `--accent` | `--accent-hover` | `--accent-foreground` |
| secondary | `--default` | `--default-hover` | `--accent-soft-foreground` |
| ghost / outline | transparent | `--default` | `--default-foreground` |
| danger | `--danger` | `--danger-hover` | `--danger-foreground` |
| danger-soft | `--danger-soft` | `--danger-soft-hover` | `--danger-soft-foreground` |
| success | `--success` | `--success-hover` | `--success-foreground` |

尺寸：`sm` h-9/md:h-8 px-3；`lg` h-11/md:h-10 text-16px。active scale：sm 0.98 / md 0.97 / lg 0.96。
图标按钮：`w-10 md:w-9 p-0`（sm: w-9/md:w-8；lg: w-11/md:w-10）。

### 卡片 `.gd-card`
```
display flex; flex-direction column; gap: 12px; padding: 16px
border-radius: min(32px, 24px)
box-shadow: var(--surface-shadow)
background: var(--surface)
```
- 标题 `text-sm(14px) leading-6 font-weight 500 color var(--foreground)`
- 描述 `text-sm leading-5 color var(--muted)`
- 变体：`--default` bg surface；`--secondary` bg surface-secondary；`--tertiary` bg surface-tertiary；`--transparent` 无 bg/无 shadow。

### 输入框 `.gd-input`
```
border-radius: var(--radius-field)  /* 12px */
padding: 8px 12px
font-size: 16px (sm 断点以上 14px)
background: var(--field-background); color: var(--field-foreground)
box-shadow: var(--field-shadow)
border-width: 0; border-color: transparent
transition: background-color 150ms var(--ease-smooth),
            border-color 150ms var(--ease-smooth),
            box-shadow 150ms var(--ease-out)
::placeholder → color var(--field-placeholder)
:hover:not(:focus) → background var(--field-hover); border-color var(--field-border-hover)
:focus → background var(--field-focus); border-color var(--field-border-focus); + focus ring
[data-invalid] → 用 --danger 系 ring
```
label `text-sm font-weight 500`；错误文案用 `--danger`。

### 排版 `.gd-prose`
按 HeroUI typography：`h1 36px/600 tracking-tight`、`h2 30px`、`h3 24px`、`h4 20px`、`h5 18px`、`h6 16px`，
`p 16px leading-7`，`code` 用 `--default` 底 + 圆角 6px，`a` 用 `--link` 且 `underline-offset-4`。

## 3. 焦点环与禁用态（HeroUI 的 status-* 工具）

```
:focus-visible {
  outline: 2px solid var(--focus);        /* --focus = --accent */
  outline-offset: var(--ring-offset-width); /* 2px */
}
[disabled], [aria-disabled="true"] {
  opacity: var(--disabled-opacity);  /* 0.5 */
  cursor: var(--cursor-disabled);    /* not-allowed */
}
```

## 4. 动效

只用 HeroUI 自带的缓动令牌，不要自己写贝塞尔：
`--ease-smooth: ease`、`--ease-out-fluid: cubic-bezier(0.32,0.72,0,1)`（Apple 式快起慢停，用于抽屉/弹层）、
`--ease-out-quart/cubic/expo` 等见 `docs/design-tokens.css`。
**务必**加 `@media (prefers-reduced-motion: reduce) { transition: none }`（HeroUI 用 `motion-reduce:` 表达了同一意图）。

## 5. 阴影

```
--surface-shadow: 0 2px 4px 0 rgba(0,0,0,.04), 0 1px 2px 0 rgba(0,0,0,.06), 0 0 1px 0 rgba(0,0,0,.06);
--overlay-shadow: 0 2px 8px 0 rgba(0,0,0,.06), 0 -6px 12px 0 rgba(0,0,0,.03), 0 14px 28px 0 rgba(0,0,0,.08);
--field-shadow:   同 surface-shadow
```
暗色主题下 HeroUI 把 shadow 置为 inset/透明，见令牌文件。

## 6. 布局基调

- 页面底 `--background`（浅灰 oklch(0.9702 0 0)），卡片/面板 `--surface`（纯白）→ 自然的浅灰底 + 白卡层次。
- 分隔用 `--separator`（oklch 92%），描边用 `--border`（oklch 90%）。
- 文字层级：主 `--foreground`、次 `--muted`。

## 7. 与公文预览的关系（不要混淆）

**应用 UI 用 HeroUI 令牌；公文纸张预览区域必须用 `gbstandard.Spec` 的数值**（A4 210×297mm、
页边距 28/26/37/35mm、正文三号 16pt 仿宋、固定行距 28.99pt）。两者不能混用：
预览区是「纸」，要严格还原国标；应用外壳是「软件界面」，用 HeroUI 语言。
预览区应当视觉上明确是「一张纸」（白底、投影、四周留白灰底衬托）。

## 8. 交付要求

- `web/src/styles/` 拆分：`tokens.css`（§1 令牌）、`base.css`（reset + 焦点环 + 动效偏好）、
  `components.css`（§2 组件配方）、`layout.css`（页面骨架）。
- 严禁出现 `@apply`、Tailwind 指令、任何 UI 库 import。
- 支持 `prefers-color-scheme` 或 `[data-theme="dark"]` 切换（令牌已备好，作为加分项）。
