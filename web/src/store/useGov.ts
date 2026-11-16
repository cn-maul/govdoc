import { useSyncExternalStore } from 'react'
import { getVersion, subscribe } from './reactive'

/**
 * 订阅状态中心的版本号。
 *
 * 组件在函数体顶部调用一次即可：任何对 gov 的深层写入都会触发重渲染，
 * 而组件始终读取同一份模块级对象，无需选择器或快照拷贝。
 */
export function useGov(): void {
  useSyncExternalStore(subscribe, getVersion, getVersion)
}
