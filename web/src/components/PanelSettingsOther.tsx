/**
 * 其他设置：与「模型怎么连」无关的应用级偏好。
 *
 * 行文方向决定发文机关标志距版心上边缘的距离（上行文 80mm，下行文与平行文
 * 35mm），它描述的是这份文件发给谁，不是模型连接的一部分。把它留在模型页会让
 * 人误以为换模型就要重选行文方向，也让「模型连接」这张卡片混进了版式设置。
 */
import { useState } from 'react'
import { Button, ToggleButton, ToggleButtonGroup } from '@heroui/react'
import type { Direction } from '../api/types'
import { gov, saveConfig } from '../store/govdoc'
import { useGov } from '../store/useGov'
import {
  CheckboxField,
  Field,
  InlineSpinner,
  SectionCard,
  TextInput,
} from '../ui/primitives'

const DIRECTIONS: Direction[] = ['下行文', '上行文', '平行文']

/** 三档改写幅度：按原文的口语程度分别给上限。 */
interface BudgetDraft {
  formal: string
  mixed: string
  colloquial: string
}

function budgetDraftOf(): BudgetDraft {
  const r = gov.config?.rewrite
  return {
    formal: String(r?.formal_max ?? 15),
    mixed: String(r?.mixed_max ?? 35),
    colloquial: String(r?.colloquial_max ?? 100),
  }
}

function parseBudget(raw: string): number | null {
  const n = Number(raw.trim())
  if (!Number.isFinite(n)) return null
  const v = Math.round(n)
  return v >= 0 && v <= 100 ? v : null
}

export default function PanelSettingsOther() {
  useGov()

  const [direction, setDirection] = useState<Direction>(
    (gov.config?.direction ?? '下行文') as Direction,
  )
  const [allowEdits, setAllowEdits] = useState(gov.config?.allow_edits ?? false)
  const [budget, setBudget] = useState<BudgetDraft>(budgetDraftOf)
  const [error, setError] = useState('')

  async function submit(): Promise<void> {
    const formal = parseBudget(budget.formal)
    const mixed = parseBudget(budget.mixed)
    const colloquial = parseBudget(budget.colloquial)
    if (formal === null || mixed === null || colloquial === null) {
      setError('三档改写幅度都必须填 0–100 之间的整数。')
      return
    }
    if (formal > mixed || mixed > colloquial) {
      setError('改写幅度应逐档放宽：已规范 ≤ 一般书面 ≤ 偏口语。')
      return
    }
    setError('')
    await saveConfig({
      direction,
      allowEdits,
      rewrite: {
        enabled: true,
        formal_max: formal,
        mixed_max: mixed,
        colloquial_max: colloquial,
      },
    })
  }

  return (
    <SectionCard
      footer={
        <div className="flex justify-end">
          <Button
            type="button"
            variant="primary"
            isDisabled={gov.savingConfig}
            onPress={() => {
              void submit()
            }}
          >
            {gov.savingConfig && <InlineSpinner />}
            {gov.savingConfig ? '保存中…' : '保存设置'}
          </Button>
        </div>
      }
    >
      <Field label="行文方向">
        <ToggleButtonGroup
          aria-label="行文方向"
          selectionMode="single"
          selectedKeys={[direction]}
          onSelectionChange={(keys) => {
            // 单选组在用户重按已选项时可能给出空集；保持原值而不是退回默认。
            const next = [...keys][0]
            if (next != null) setDirection(String(next) as Direction)
          }}
          className="self-start"
        >
          {DIRECTIONS.map((d) => (
            <ToggleButton key={d} id={d} size="sm" className="px-3">
              {d}
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
      </Field>

      <CheckboxField
        isSelected={allowEdits}
        onChange={setAllowEdits}
        label="允许模型修正错别字、语病与用词"
      />

      {/*
        三个数字是「尽量少改动」的执行标准：文档哪一段越口语，允许改动的比例
        越大；已经规范的段落只允许小修。它们是上限，不是目标 —— 模型没必要时
        不会改满。
      */}
      <section className="space-y-3 rounded-lg border border-border px-4 py-3.5">
        <div>
          <h4 className="text-sm font-semibold">改写幅度上限</h4>
          <p className="mt-1 text-xs leading-relaxed text-muted">
            按原文的口语程度分级。数字越大，允许改写的比例越高；超过上限的段落会被拒绝导出，
            并提示是哪一段、改了多少。
          </p>
        </div>

        <div className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-3">
          <Field label="已规范段落最多改动（%）" hint="已经很公文的段落，只允许小修。">
            <TextInput
              type="number"
              min="0"
              max="100"
              step="1"
              value={budget.formal}
              onChange={(e) => setBudget({ ...budget, formal: e.target.value })}
            />
          </Field>
          <Field label="一般书面段落最多改动（%）" hint="普通书面语，允许适度调整措辞与句序。">
            <TextInput
              type="number"
              min="0"
              max="100"
              step="1"
              value={budget.mixed}
              onChange={(e) => setBudget({ ...budget, mixed: e.target.value })}
            />
          </Field>
          <Field
            label="偏口语段落最多改动（%）"
            hint="口语很重的段落可以大改。填 100 表示不按字面比例限制，仍会检查数字、日期等事实。"
          >
            <TextInput
              type="number"
              min="0"
              max="100"
              step="1"
              value={budget.colloquial}
              onChange={(e) => setBudget({ ...budget, colloquial: e.target.value })}
            />
          </Field>
        </div>

        {error && <div className="text-xs text-danger">{error}</div>}
      </section>
    </SectionCard>
  )
}
