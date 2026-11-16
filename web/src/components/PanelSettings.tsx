/**
 * 设置页：模型连接 + 排版规则管理 + 其他设置。
 *
 * 独立成页而不是塞进上传区，是因为这两样都是**应用级**设置，与某一份文档无关。
 * 放在上传区会让人以为换个文件就要重填一次 —— 那是真的每传一份文档都会重填。
 *
 * 页内再分成三个子页签：模型连接、排版规则、其他设置是三类完全不同的东西
 * （一个是密钥与接口，一个是字体字号页边距，一个是行文方向与校对偏好），
 * 并排堆在一页里会让「我只想改 API Key」的人先滚过二十多行版式规则。
 */
import { useState } from 'react'
import { Tabs } from '@heroui/react'
import PanelSettingsAI from './PanelSettingsAI'
import PanelSettingsProfile from './PanelSettingsProfile'
import PanelSettingsOther from './PanelSettingsOther'

type SettingsTab = 'model' | 'profiles' | 'other'

export default function PanelSettings() {
  const [tab, setTab] = useState<SettingsTab>('model')

  return (
    <section className="flex min-w-0 flex-col gap-5">
      <Tabs
        aria-label="设置分类"
        selectedKey={tab}
        onSelectionChange={(key) => setTab(String(key) as SettingsTab)}
        className="w-full"
      >
        {/*
          w-fit min-w-0：HeroUI 的横向 Tabs 默认让列表铺满整行（min-w-full），
          每个页签各自 50%。在一张宽卡片顶上，两个页签会被拉成两段相隔很远的
          文字。这里让它们按文字宽度排在一起。
        */}
        <Tabs.List className="w-fit min-w-0">
          <Tabs.Tab id="model" className="w-auto whitespace-nowrap px-3">
            模型连接
          </Tabs.Tab>
          <Tabs.Tab id="profiles" className="w-auto whitespace-nowrap px-3">
            排版规则
          </Tabs.Tab>
          <Tabs.Tab id="other" className="w-auto whitespace-nowrap px-3">
            其他设置
          </Tabs.Tab>
        </Tabs.List>

        {/*
          shouldForceMount：两个子页里都有未提交的表单（模型表单、规则草稿）。
          RAC 默认在切走时卸载面板，那样用户填了一半 API Key、去「排版规则」看一眼
          再切回来，输入框就已经被重置了 —— 而这些输入本来就该在切换页签时保留。

          force mount 之后 RAC 不再卸载未选中的面板，只是给它加 inert；隐藏得自己
          写（下面那个 data-[inert=true]:hidden）—— 少了它，两个子页会同时显示。
        */}
        <Tabs.Panel id="model" shouldForceMount className="pt-3 data-[inert=true]:hidden">
          <PanelSettingsAI />
        </Tabs.Panel>
        <Tabs.Panel id="profiles" shouldForceMount className="pt-3 data-[inert=true]:hidden">
          <PanelSettingsProfile />
        </Tabs.Panel>
        <Tabs.Panel id="other" shouldForceMount className="pt-3 data-[inert=true]:hidden">
          <PanelSettingsOther />
        </Tabs.Panel>
      </Tabs>
    </section>
  )
}
