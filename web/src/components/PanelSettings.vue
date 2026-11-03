<script setup lang="ts">
/**
 * 设置页：AI 设置 + 排版规则管理。
 *
 * 单独一页而不是塞进上传区，是因为这两样都是**应用级**设置，与某一份文档无关。
 * 放在上传区会让人以为换个文件就要重填一次 —— 那是真的每传一份文档都会重填。
 */
import PanelSettingsAI from './PanelSettingsAI.vue'
import PanelSettingsProfile from './PanelSettingsProfile.vue'
</script>

<template>
  <section class="gd-work">
    <header class="gd-section-title">
      <h2>设置</h2>
      <p>模型连接与排版规则。两者都保存到程序所在目录，重启后仍然有效。</p>
    </header>

    <div class="gd-stack">
      <PanelSettingsProfile />
      <PanelSettingsAI />
    </div>
  </section>
</template>