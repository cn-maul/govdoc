/**
 * GovDoc 前端入口。
 *
 * 样式顺序有意义：tokens（设计令牌）→ base（重置）→ components（组件配方）
 * → layout（页面骨架，可以覆盖前者的布局属性）。
 */
import { createApp } from 'vue'
import App from './App.vue'

import './styles/tokens.css'
import './styles/base.css'
import './styles/components.css'
import './styles/layout.css'

createApp(App).mount('#app')
