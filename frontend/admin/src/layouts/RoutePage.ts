import { cloneVNode, defineComponent, inject, provide, shallowReactive, watch, type PropType, type VNode } from 'vue'
import { routeLocationKey, type RouteLocationNormalizedLoaded } from 'vue-router'

export const ROUTE_CACHE_LIMIT = 10

/** 复制路由字段，避免隐藏页读取其他标签参数。@param route 当前路由 @returns 隔离快照 */
function snapshotRoute(route: RouteLocationNormalizedLoaded): RouteLocationNormalizedLoaded {
  return { ...route, meta: { ...route.meta }, query: { ...route.query }, params: { ...route.params }, matched: [...route.matched] }
}

/** 移植舒课 RoutePage：按实际路径隔离 useRoute，定义与 KeepAlive 均有界。
 * @param max 最大定义数量 @returns 页面包装器集合
 */
export function createRoutePageCache(max = ROUTE_CACHE_LIMIT) {
  const wrappers = new Map<string, ReturnType<typeof defineComponent>>()
  return {
    clear() { wrappers.clear() },
    retain(paths: string[]) {
      for (const path of wrappers.keys()) if (!paths.includes(path)) wrappers.delete(path)
    },
    get(path: string) {
      let wrapper = wrappers.get(path)
      if (!wrapper) {
        wrapper = defineComponent({
          name: path,
          props: {
            vnode: { type: Object as PropType<VNode>, required: true },
            route: { type: Object as PropType<RouteLocationNormalizedLoaded>, required: true },
          },
          setup(props) {
            const liveRoute = inject(routeLocationKey)
            const frozenRoute = shallowReactive(snapshotRoute(props.route))
            provide(routeLocationKey, frozenRoute)
            watch(() => liveRoute?.fullPath, () => {
              if (liveRoute?.path === path) Object.assign(frozenRoute, snapshotRoute(liveRoute))
            }, { flush: 'sync' })
            watch(() => props.route, route => {
              if (route.path === path) Object.assign(frozenRoute, snapshotRoute(route))
            }, { flush: 'sync' })
            return () => cloneVNode(props.vnode)
          },
        })
      }
      wrappers.delete(path)
      wrappers.set(path, wrapper)
      while (wrappers.size > max) wrappers.delete(wrappers.keys().next().value!)
      return wrapper
    },
  }
}
