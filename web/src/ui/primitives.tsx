/**
 * HeroUI 组件适配层。
 *
 * 业务组件只从这里取展示件，不直接散落拼装。这一层存在的理由不是"再包一层"，
 * 而是三件 HeroUI v3 自己不便表达的事：
 *
 *   1. `Btn` —— react-aria-components 的 `filterDOMProps` 会丢掉原生 `title`，
 *      而本项目大量按钮靠 title 说明「为什么禁用」。用一个 inline-flex 的 span
 *      承接提示文案，其余 props 原样透传。
 *   2. `Select` —— HeroUI v3 的 Select 是 RAC 复合组件（Trigger / Popover /
 *      ListBox 三件套），每个调用点都手写一遍既啰嗦又容易漏掉键盘可达性。
 *      这里收成 `value` + `onChange` + `items` 的单值选择器。
 *   3. `Field` —— 标签、可选项、提示与错误的统一排版；标签用 HeroUI 的 `Label`。
 *
 * 其余（Input / TextArea / Chip / Alert / Card / EmptyState / Spinner /
 * ProgressBar / Checkbox）一律直接用 HeroUI，只补默认样式与中文排版间距。
 */
import type { ComponentProps, ReactNode } from 'react'
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Chip,
  Description,
  EmptyState as HeroEmptyState,
  Input,
  Label,
  Link as HeroLink,
  ListBox,
  ProgressBar as HeroProgressBar,
  Select as HeroSelect,
  Spinner,
  TextArea as HeroTextArea,
  cn,
} from '@heroui/react'

/**
 * HeroUI 的 Button 基于 react-aria-components，后者用 filterDOMProps 把原生
 * `title` 过滤掉（只放行 id / aria-* / data-*），所以直接写 `title="…"` 既不
 * 会出现在 DOM 上，也过不了类型检查。这里用一个带 title 的 inline-flex 包裹层
 * 承接提示文案，其余 props 原样透传。
 */
export function Btn({
  title,
  ...props
}: ComponentProps<typeof Button> & { title?: string }) {
  const button = <Button {...props} />
  if (!title) return button
  return (
    <span title={title} className="inline-flex">
      {button}
    </span>
  )
}

/**
 * 长得像 Button 的链接（下载、新窗口预览）。
 *
 * 用 `<a>` 而不是 `onPress` 调 `window.open`：浏览器对链接的判断更准——
 * 中键新标签、右键「另存为」、拖到文件夹、地址栏悬停显示 URL，这些能力只有
 * 真正的链接才有，用按钮加脚本模拟是拿不到的。
 *
 * 视觉上复用 HeroUI 自己的按钮样式类（`.button` + 尺寸/变体修饰符），而不是
 * 在调用点手写一套 rounded/bg/hover：手写的那套一旦与组件库主题改动脱节，
 * 下载按钮就会和旁边的主按钮长得不一样。
 */
export function LinkButton({
  href,
  download,
  target,
  size = 'md',
  variant = 'primary',
  className,
  children,
  title,
}: {
  href: string
  download?: boolean
  target?: string
  size?: 'sm' | 'md' | 'lg'
  variant?: 'primary' | 'secondary' | 'ghost' | 'tertiary' | 'outline'
  className?: string
  children: ReactNode
  title?: string
}) {
  const link = (
    <HeroLink
      href={href}
      download={download}
      target={target}
      rel={target === '_blank' ? 'noopener' : undefined}
      className={cn('button', `button--${size}`, `button--${variant}`, className)}
    >
      {children}
    </HeroLink>
  )
  if (!title) return link
  return (
    <span title={title} className="inline-flex">
      {link}
    </span>
  )
}

/** 输入框：HeroUI 的 Input 本身就是原生 <input> 的受控封装，value/onChange 签名不变。 */
export function TextInput({ className, ...props }: ComponentProps<typeof Input>) {
  return <Input className={cn('w-full', className)} {...props} />
}

/** 多行输入。 */
export function TextArea({ className, ...props }: ComponentProps<typeof HeroTextArea>) {
  return <HeroTextArea className={cn('w-full min-h-16 resize-y', className)} {...props} />
}

export interface SelectItem {
  value: string
  label: string
  /** 选中目标里的次要说明（例如行距规则的含义）。 */
  hint?: string
  isDisabled?: boolean
}

/**
 * 单值选择器。
 *
 * 保留调用点熟悉的心智：`value` 为空串时显示 placeholder，选择后回调的是
 * 字符串而不是 React Aria 的 Key。内部仍是 HeroUI 的 Select 复合组件，
 * 所以键盘、滚动与浮层定位都是组件库自己的行为，不由本项目重写。
 */
export function Select({
  value,
  onChange,
  items,
  placeholder,
  isDisabled,
  className,
  'aria-label': ariaLabel,
  id,
  compact = false,
}: {
  value: string
  onChange: (value: string) => void
  items: SelectItem[]
  placeholder?: string
  isDisabled?: boolean
  className?: string
  'aria-label'?: string
  id?: string
  /** 行内小尺寸（校对区的批量/移动控件）。 */
  compact?: boolean
}) {
  return (
    <HeroSelect
      id={id}
      aria-label={ariaLabel}
      className={cn('w-full', compact && 'text-xs', className)}
      selectedKey={value === '' ? null : value}
      onSelectionChange={(key) => onChange(key == null ? '' : String(key))}
      placeholder={placeholder ?? '请选择'}
      isDisabled={isDisabled}
    >
      <HeroSelect.Trigger className={cn('w-full', compact && 'min-h-7 py-0.5 text-xs')}>
        <HeroSelect.Value />
        <HeroSelect.Indicator />
      </HeroSelect.Trigger>
      <HeroSelect.Popover>
        <ListBox>
          {items.map((it) => (
            <ListBox.Item key={it.value} id={it.value} textValue={it.label} isDisabled={it.isDisabled}>
              <span className="flex min-w-0 flex-col">
                <span>{it.label}</span>
                {it.hint && <span className="text-xs text-muted">{it.hint}</span>}
              </span>
            </ListBox.Item>
          ))}
        </ListBox>
      </HeroSelect.Popover>
    </HeroSelect>
  )
}

/**
 * 复选开关 + 说明。
 *
 * HeroUI 的 Checkbox 是 Field + Content + Control + Indicator 四件套：
 * 说明文字必须放在 Content 之外的兄弟位置，否则会被读成标签的一部分。
 */
export function CheckboxField({
  isSelected,
  onChange,
  isDisabled,
  label,
  description,
}: {
  isSelected: boolean
  onChange: (checked: boolean) => void
  isDisabled?: boolean
  label: ReactNode
  description?: ReactNode
}) {
  return (
    <Checkbox isSelected={isSelected} onChange={onChange} isDisabled={isDisabled}>
      <Checkbox.Content>
        <Checkbox.Control>
          <Checkbox.Indicator />
        </Checkbox.Control>
        <span className="text-sm font-medium">{label}</span>
      </Checkbox.Content>
      {description && (
        <Description className="text-xs leading-relaxed text-muted">{description}</Description>
      )}
    </Checkbox>
  )
}

export type AlertStatus = 'info' | 'success' | 'warning' | 'danger'

const ALERT_STATUS: Record<AlertStatus, 'accent' | 'success' | 'warning' | 'danger'> = {
  info: 'accent',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
}

export function SectionCard({
  title,
  description,
  actions,
  children,
  footer,
  compact = false,
  className,
}: {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children?: ReactNode
  footer?: ReactNode
  compact?: boolean
  className?: string
}) {
  return (
    <Card className={cn('w-full', compact && 'gap-3', className)}>
      {(title || description || actions) && (
        <Card.Header className="flex flex-row items-start justify-between gap-4">
          <div className="min-w-0 space-y-1">
            {title && <Card.Title className="text-base font-semibold">{title}</Card.Title>}
            {description && (
              <Card.Description className="text-sm text-muted">{description}</Card.Description>
            )}
          </div>
          {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
        </Card.Header>
      )}
      {children && (
        <Card.Content className={cn('space-y-3', compact && 'space-y-2')}>{children}</Card.Content>
      )}
      {footer && <Card.Footer>{footer}</Card.Footer>}
    </Card>
  )
}

export function AlertBox({
  status,
  title,
  children,
  className,
}: {
  status: AlertStatus
  title?: ReactNode
  children?: ReactNode
  className?: string
}) {
  return (
    <Alert status={ALERT_STATUS[status]} className={className}>
      <Alert.Indicator />
      <Alert.Content>
        {title && <Alert.Title>{title}</Alert.Title>}
        {children != null && (
          <Alert.Description className="block whitespace-pre-wrap">{children}</Alert.Description>
        )}
      </Alert.Content>
    </Alert>
  )
}

export function Badge({
  color = 'default',
  variant = 'soft',
  children,
  className,
  title,
}: {
  color?: 'default' | 'accent' | 'success' | 'warning' | 'danger'
  variant?: 'primary' | 'secondary' | 'soft' | 'tertiary'
  children: ReactNode
  className?: string
  title?: string
}) {
  return (
    <Chip color={color} variant={variant} size="sm" className={className} title={title}>
      {children}
    </Chip>
  )
}

/**
 * 表单字段：标签 + 控件 + 说明 + 错误。
 *
 * 标签用的是 HeroUI 的 `Label`（真 `<label>`，带组件库的排版），点击仍然聚焦
 * 到 `htmlFor` 指定的控件；说明与错误放在控件后面 —— 与 react-aria 的字段顺序
 * 一致，屏幕阅读器会连着读完。
 */
export function Field({
  label,
  htmlFor,
  hint,
  error,
  optional,
  children,
  className,
}: {
  label?: ReactNode
  htmlFor?: string
  hint?: ReactNode
  error?: ReactNode
  optional?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      {label && (
        <Label htmlFor={htmlFor} className="flex flex-wrap items-baseline gap-2 text-sm font-medium">
          <span>{label}</span>
          {optional && <span className="text-xs font-normal text-muted">{optional}</span>}
        </Label>
      )}
      {children}
      {hint && <div className="text-xs leading-relaxed text-muted">{hint}</div>}
      {error && <div className="text-xs text-danger">{error}</div>}
    </div>
  )
}

export function Row({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('flex flex-wrap items-center gap-2', className)}>{children}</div>
}

export function Stack({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('flex flex-col gap-3', className)}>{children}</div>
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <HeroEmptyState>{children}</HeroEmptyState>
}

export function InlineSpinner({ className }: { className?: string }) {
  return <Spinner className={cn('size-4', className)} />
}

/**
 * 不确定进度条（分析进行中）。
 *
 * 用 HeroUI 的 ProgressBar 而不是手写一条动画 div：它自带 role="progressbar"
 * 与 aria-busy，读屏器能报告"正在分析"，而手写 div 只是一个动的色块。
 */
export function IndeterminateProgress({ label }: { label: string }) {
  return (
    <HeroProgressBar isIndeterminate aria-label={label} size="sm">
      <HeroProgressBar.Track>
        <HeroProgressBar.Fill />
      </HeroProgressBar.Track>
    </HeroProgressBar>
  )
}
