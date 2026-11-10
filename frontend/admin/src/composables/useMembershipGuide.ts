import { computed, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAdminAuthStore } from '@/stores/auth'

/** 固定业务顺序，不覆盖机器人其他管理功能。 */
export const membershipGuideSteps = [
  { key: 'groups', path: '/telegram-bot/groups' },
  { key: 'products', path: '/telegram-bot/membership/products' },
  { key: 'orders', path: '/telegram-bot/membership/orders' },
  { key: 'support', path: '/telegram-bot/support/sessions' },
  { key: 'recovery', path: '/telegram-bot/recovery' },
] as const

/** 判断是否属于会员群页面。@param path 路由路径 @returns 是否显示入口 */
export function isMembershipGuideRoute(path: string): boolean {
  return ['/telegram-bot/groups', '/telegram-bot/membership', '/telegram-bot/support', '/telegram-bot/recovery']
    .some(prefix => path === prefix || path.startsWith(`${prefix}/`))
}

/** 管理本浏览器、当前管理员的首次引导；不参与授权或业务状态。@returns 引导入口状态和操作 */
export function useMembershipGuide() {
  const route = useRoute()
  const auth = useAdminAuthStore()
  const visible = computed(() => isMembershipGuideRoute(route.path))
  const storageKey = computed(() => auth.adminId > 0 ? `admin_membership_guide_v1:${auth.adminId}` : '')
  const open = ref(false)
  const step = ref(0)
  const visited = new Set<string>()

  /** 首次展示即记录，关闭、跳过和完成都不会再次自动打扰。@returns void */
  function remember() {
    if (!storageKey.value) return
    visited.add(storageKey.value)
    try { localStorage.setItem(storageKey.value, 'seen') }
    catch { console.warn('[membership-guide] Unable to persist guide preference') }
  }

  /** 手动重新打开完整引导。@returns void */
  function show() {
    step.value = 0
    open.value = true
    remember()
  }

  watch([visible, storageKey], ([enabled, key], previous) => {
    if (!enabled || key !== previous?.[1]) open.value = false
    if (!enabled || !key || visited.has(key)) return
    visited.add(key)
    try {
      if (localStorage.getItem(key) === 'seen') return
    } catch { console.warn('[membership-guide] Unable to read guide preference') }
    show()
  }, { immediate: true })

  return { visible, open, step, show }
}
