import { inject, type ComputedRef, type InjectionKey } from 'vue'

/** 页面弹窗的内容区域，由后台缓存视图提供；站外组件仍使用全屏模态。 */
export const pageDialogTargetKey: InjectionKey<ComputedRef<HTMLElement | null>> = Symbol('pageDialogTarget')
const pageLocks = new WeakMap<HTMLElement, { count: number; previous: boolean }>()
/** 锁定页面背景键盘交互，允许顶部标签获得焦点；支持嵌套弹窗。@param host 页面挂载区 @returns 释放函数 */
export function lockPageBackground(host: HTMLElement) {
  const main = host.parentElement?.querySelector('main')
  if (!main) return () => undefined
  const state = pageLocks.get(main) ?? { count: 0, previous: main.inert }
  state.count++
  pageLocks.set(main, state)
  main.inert = true
  return () => {
    if (--state.count === 0) { main.inert = state.previous; pageLocks.delete(main) }
  }
}

export const pageDialogOpenKey: InjectionKey<ComputedRef<boolean>> = Symbol('pageDialogOpen')
export const pageDialogScopeKey: InjectionKey<ComputedRef<boolean>> = Symbol('pageDialogScope')
/** 返回页面弹窗开关与目标区域，默认保持原模态。@returns 页面范围上下文 */
export function usePageDialogScope() {
  return { scoped: inject(pageDialogScopeKey, undefined), target: inject(pageDialogTargetKey, undefined), open: inject(pageDialogOpenKey, undefined) }
}
