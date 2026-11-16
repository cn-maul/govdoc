/**
 * 极简响应式原语。
 *
 * 迁移前状态层依赖 Vue 的 reactive/computed。这里保留同样的使用形态：
 * 深层 Proxy 在写入时自增版本号并通知订阅者，computed 是惰性 getter。
 * React 组件通过 useSyncExternalStore 订阅版本号，然后直接读同一份对象。
 */

let version = 0
const listeners = new Set<() => void>()
const proxies = new WeakMap<object, object>()

export function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function getVersion(): number {
  return version
}

function notifyChange(): void {
  version += 1
  for (const listener of listeners) listener()
}

function wrap<T>(target: T): T {
  if (target === null || typeof target !== 'object') return target
  const cached = proxies.get(target as object)
  if (cached) return cached as T

  const proxy = new Proxy(target as object, {
    get(t, key, receiver) {
      return wrap(Reflect.get(t, key, receiver))
    },
    set(t, key, value, receiver) {
      const previous = Reflect.get(t, key, receiver)
      const ok = Reflect.set(t, key, value, receiver)
      if (!Object.is(previous, value)) notifyChange()
      return ok
    },
    deleteProperty(t, key) {
      const ok = Reflect.deleteProperty(t, key)
      notifyChange()
      return ok
    },
  })

  proxies.set(target as object, proxy)
  return proxy as T
}

export function reactive<T extends object>(target: T): T {
  return wrap(target)
}

export function computed<T>(getter: () => T): { readonly value: T } {
  return {
    get value() {
      return getter()
    },
  }
}
