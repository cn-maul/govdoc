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
} from '../ui/primitives'

const DIRECTIONS: Direction[] = ['下行文', '上行文', '平行文']

export default function PanelSettingsOther() {
  useGov()

  const [direction, setDirection] = useState<Direction>(
    (gov.config?.direction ?? '下行文') as Direction,
  )
  const [allowEdits, setAllowEdits] = useState(gov.config?.allow_edits ?? false)

  async function submit(): Promise<void> {
    await saveConfig({ direction, allowEdits })
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
    </SectionCard>
  )
}
