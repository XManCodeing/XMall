/** 页面引导锚点只定位现有控件，不执行点击或业务请求。 */
export interface MembershipPageStep { target: string; key: string }

/** 构造只读步骤。@param target 锚点 @param key 文案键 @returns 步骤 */
const step = (target: string, key: string): MembershipPageStep => ({ target, key })

/** 按当前页面选择引导，不把未知子页当成已支持页面。@param path 路由 @returns 有序步骤 */
export function membershipPageSteps(path: string): MembershipPageStep[] {
  const pages: Record<string, MembershipPageStep[]> = {
    '/telegram-bot/groups': [step('groups-filter', 'groupFilter'), step('groups-create', 'groupCreate'), step('groups-list', 'groupList'), step('groups-actions', 'groupActions')],
    '/telegram-bot/membership/products': [step('products-create', 'productCreate'), step('products-filter', 'productFilter'), step('products-list', 'productList'), step('products-actions', 'productActions')],
    '/telegram-bot/membership/orders': [step('orders-tabs', 'orderTabs'), step('orders-filter', 'orderFilter'), step('orders-list', 'orderList'), step('orders-actions', 'orderActions')],
    '/telegram-bot/support/sessions': [step('support-tabs', 'supportTabs'), step('support-filter', 'supportFilter'), step('support-list', 'supportList'), step('support-actions', 'supportActions')],
    '/telegram-bot/support/tasks': [step('support-tabs', 'supportTabs'), step('support-resource', 'supportResource'), step('support-filter', 'taskFilter'), step('support-list', 'taskList'), step('support-actions', 'taskActions')],
    '/telegram-bot/recovery': [step('recovery-actions', 'recoveryActions'), step('recovery-hint', 'recoveryHint'), step('recovery-list', 'recoveryList'), step('recovery-detail', 'recoveryDetail')],
  }
  if (path === '/telegram-bot/membership/orders') {
    const active = ['access', 'reviews', 'lifecycle'].find(tab => membershipGuideTarget(`orders-${tab}`))
    if (active) return [step('orders-tabs', 'orderTabs'), step(`orders-${active}`, `order${active[0]?.toUpperCase()}${active.slice(1)}`)]
  }
  return pages[path] ?? []
}

/** 找到当前可见实例，排除缓存、隐藏 Tab 和无权限入口。@param target 锚点名 @returns 可见元素 */
export function membershipGuideTarget(target: string): HTMLElement | undefined {
  return [...document.querySelectorAll<HTMLElement>(`[data-guide="${target}"]`)]
    .find(element => element.getClientRects().length > 0 && getComputedStyle(element).visibility !== 'hidden')
}
