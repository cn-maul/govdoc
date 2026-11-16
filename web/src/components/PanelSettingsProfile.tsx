/**
 * 排版规则（profile）管理。
 *
 * 软件因此是通用的：公文只是其中一套内置规则，换一套就换掉标题/正文/行间距/
 * 字体/字号/页边距 —— 渲染管线不变，变的只是这张规则表。
 *
 * 关于「内置不可改」：与其让用户点了再吃一个 400，不如把按钮**禁用**并把原因
 * 写在旁边。内置规则是所有文档的兜底，也是恢复标准样式的唯一依据，所以真正
 * 该做的不是解禁，而是把「复制一份」放到最显眼的位置。
 */
import { useMemo, useState } from 'react'
import { Button, Table, ToggleButton, ToggleButtonGroup } from '@heroui/react'
import { specSummary, type Profile, type Spec } from '../api/types'
import SpecEditor from './SpecEditor'
import { canDelete, cloneSpec, isBuiltin, suggestCopyName, validateSpec } from '../lib/specEdit'
import {
  copyProfile,
  deleteProfile,
  gov,
  notify,
  refreshProfiles,
  saveProfile,
  setActiveProfile,
} from '../store/govdoc'
import { useGov } from '../store/useGov'
import { Badge, Field, Row, SectionCard, TextInput } from '../ui/primitives'

export default function PanelSettingsProfile() {
  useGov()

  /** 正在编辑的 profile id；null 表示没在编辑。 */
  const [editingId, setEditingId] = useState<string | null>(null)
  /** 编辑中的规则副本（改动先落在这里，点保存才提交）。 */
  const [draft, setDraft] = useState<Spec | null>(null)
  /** 编辑中的名称。 */
  const [draftName, setDraftName] = useState('')

  const list = gov.profiles
  const activeId = gov.activeProfile

  const editing = useMemo<Profile | null>(
    () => list.find((p) => p.id === editingId) ?? null,
    [list, editingId],
  )

  function openEditor(p: Profile): void {
    setEditingId(p.id)
    setDraft(cloneSpec(p.spec))
    setDraftName(p.name)
  }

  function closeEditor(): void {
    setEditingId(null)
    setDraft(null)
    setDraftName('')
  }

  function onDraftChange(next: Spec): void {
    setDraft(next)
  }

  const draftProblems = useMemo(() => (draft ? validateSpec(draft) : []), [draft])

  async function onSaveDraft(): Promise<void> {
    if (!editing || !draft) return
    const ok = await saveProfile({
      id: editing.id,
      name: draftName.trim() || editing.name,
      description: editing.description ?? '',
      spec: draft,
    })
    if (ok) closeEditor()
  }

  /** 新建一份空白配置：从「公文」起手，而不是从零。 */
  const [newName, setNewName] = useState('')

  async function onCreateBlank(): Promise<void> {
    const name = newName.trim()
    if (name === '') {
      notify('error', '请先给新配置起个名字。')
      return
    }
    const created = await copyProfile('govdoc', name, '基于公文规则新建')
    setNewName('')
    if (created) openEditor(created)
  }

  function onCopy(p: Profile): void {
    const name = suggestCopyName(p.name)
    setNewName(name)
    void copyProfile(p.id, name, p.description ?? '').then((c) => {
      if (c) {
        setNewName('')
        openEditor(c)
      }
    })
  }

  async function onActivate(p: Profile): Promise<void> {
    await setActiveProfile(p.id)
    // 让「哪套是默认」立刻反映到上传区的选择上。
    gov.selectedProfile = p.id
  }

  async function onDelete(p: Profile): Promise<void> {
    const gate = canDelete(p, activeId)
    if (!gate.ok) {
      notify('error', gate.why)
      return
    }
    if (!window.confirm(`确定删除「${p.name}」吗？此操作不可撤销。`)) return
    if (editingId === p.id) closeEditor()
    await deleteProfile(p.id)
  }

  async function onReload(): Promise<void> {
    closeEditor()
    await refreshProfiles()
  }

  return (
    <SectionCard
      actions={
        <Button
          type="button"
          size="sm"
          variant="ghost"
          isDisabled={gov.savingProfile}
          onPress={() => {
            void onReload()
          }}
        >
          刷新
        </Button>
      }
    >
      {/* 表格用 HeroUI 的 Table：列头、行、单元格都是组件库的复合件，
          列宽由 flex 分配，窄屏自动横向滚动（ScrollContainer 的默认行为）。 */}
      <Table aria-label="排版规则列表">
        <Table.ScrollContainer>
          <Table.Content aria-label="排版规则列表">
            <Table.Header>
              <Table.Column isRowHeader>名称</Table.Column>
              <Table.Column>规则摘要</Table.Column>
              <Table.Column>操作</Table.Column>
            </Table.Header>
            <Table.Body
              items={list}
              renderEmptyState={() => (
                <div className="px-3 py-2 text-sm text-muted">暂无排版规则。</div>
              )}
            >
              {(p: Profile) => {
                const gate = canDelete(p, activeId)
                return (
                  <Table.Row key={p.id} id={p.id}>
                    <Table.Cell>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <strong>{p.name}</strong>
                        {p.id === activeId && <Badge color="accent">默认</Badge>}
                        {isBuiltin(p) && <Badge>内置</Badge>}
                      </div>
                    </Table.Cell>
                    <Table.Cell className="max-w-[30ch] whitespace-normal text-[13px] leading-relaxed text-muted">
                      {specSummary(p.spec)}
                    </Table.Cell>
                    <Table.Cell>
                      <Row className="gap-1.5">
                        {!isBuiltin(p) && (
                          <Button
                            type="button"
                            size="sm"
                            variant="secondary"
                            onPress={() => openEditor(p)}
                          >
                            编辑
                          </Button>
                        )}
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          isDisabled={gov.savingProfile}
                          onPress={() => onCopy(p)}
                        >
                          复制一份
                        </Button>
                        <span title={gate.ok ? '删除这份配置' : gate.why}>
                          <Button
                            type="button"
                            size="sm"
                            variant="ghost"
                            isDisabled={gov.savingProfile || p.id === activeId}
                            onPress={() => {
                              void onDelete(p)
                            }}
                          >
                            删除
                          </Button>
                        </span>
                      </Row>
                    </Table.Cell>
                  </Table.Row>
                )
              }}
            </Table.Body>
          </Table.Content>
        </Table.ScrollContainer>
      </Table>

      <Field label="默认规则">
        <ToggleButtonGroup
          aria-label="默认规则"
          selectionMode="single"
          selectedKeys={[activeId]}
          isDisabled={gov.savingProfile}
          onSelectionChange={(keys) => {
            const next = [...keys][0]
            const p = list.find((x) => x.id === String(next))
            if (p && p.id !== activeId) void onActivate(p)
          }}
          className="self-start"
        >
          {list.map((p) => (
            <ToggleButton key={p.id} id={p.id} size="sm" className="px-3">
              {p.name}
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
      </Field>

      <Field label="新建一份配置">
        <Row className="gap-1.5">
          <TextInput
            type="text"
            placeholder="例如：公司报告、投稿须知"
            autoComplete="off"
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            className="min-w-0 flex-1"
          />
          <Button
            type="button"
            variant="primary"
            isDisabled={gov.savingProfile || newName.trim() === ''}
            onPress={() => {
              void onCreateBlank()
            }}
          >
            新建
          </Button>
        </Row>
      </Field>

      {/* 编辑器 */}
      {editing && draft && (
        <div className="mt-4 flex flex-col gap-3 rounded-lg border border-border bg-surface-secondary p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <div className="text-sm font-medium">编辑「{draftName || editing.name}」</div>
            </div>
          </div>

          <Field label="名称">
            <TextInput
              type="text"
              autoComplete="off"
              value={draftName}
              onChange={(e) => setDraftName(e.target.value)}
            />
          </Field>

          <SpecEditor spec={draft} readOnly={isBuiltin(editing)} onChange={onDraftChange} />

          <Row className="justify-end">
            <Button type="button" variant="ghost" onPress={closeEditor}>
              放弃
            </Button>
            <Button
              type="button"
              variant="primary"
              isDisabled={gov.savingProfile || draftProblems.length > 0}
              onPress={() => {
                void onSaveDraft()
              }}
            >
              {gov.savingProfile ? '保存中…' : '保存规则'}
            </Button>
          </Row>
        </div>
      )}
    </SectionCard>
  )
}
