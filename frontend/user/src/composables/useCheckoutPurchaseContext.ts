import { onBeforeRouteLeave } from 'vue-router'
import type { CartItem } from '../stores/cart'
import { useUserAuthStore } from '../stores/userAuth'
import { clearPurchaseContext, purchaseSelection, savePurchaseContext } from '../utils/purchaseContext'

/** 为共享结算登记离站上下文。getItems 只提供选择，不保存表单、金额、支付选项。 */
export function useCheckoutPurchaseContext(getItems: () => CartItem[]) {
  const auth = useUserAuthStore()
  onBeforeRouteLeave((to, from) => {
    if (to.path !== '/me/security') return
    clearPurchaseContext()
    if (!auth.isAuthenticated) return
    const items = getItems().map(purchaseSelection)
    if (items.some((item) => !item)) return
    savePurchaseContext({ version: 1, accountId: Number(auth.user?.id), issuedAt: Date.now(),
      returnPath: from.query.mode === 'buynow' ? '/checkout?mode=buynow' : '/checkout',
      items: items.filter((item) => item !== null),
    })
  })
}
