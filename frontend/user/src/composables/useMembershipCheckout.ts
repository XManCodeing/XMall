import { computed, ref } from 'vue'
import { useUserAuthStore } from '../stores/userAuth'
import type { CartItem } from '../stores/cart'
import { refreshCartStockSnapshots } from '../utils/cartStock'
import { membershipSalesBlocked } from '../utils/membershipPurchase'

/** Revalidate public catalog before quoting/submitting; local membership flags never authorize checkout. */
export function useMembershipCheckout(getItems: () => CartItem[]) {
  const auth = useUserAuthStore()
  const checking = ref(false)
  const failed = ref(false)
  let revision = 0
  const blocked = computed(() => failed.value || checking.value || getItems().some(membershipSalesBlocked))

  /** Fetch exact current SKU projections without rewriting quantities or mixed cart selections. */
  async function refresh(): Promise<boolean> {
    const request = ++revision
    const accountId = auth.user?.id
    const authenticated = auth.isAuthenticated
    const items = getItems()
    const signature = JSON.stringify(items.map(({ productId, skuId, quantity }) => [productId, skuId, quantity]))
    const current = () => request === revision && auth.user?.id === accountId && auth.isAuthenticated === authenticated
      && signature === JSON.stringify(getItems().map(({ productId, skuId, quantity }) => [productId, skuId, quantity]))
    checking.value = true
    failed.value = false
    try {
      await refreshCartStockSnapshots({ items, patchItem: (id, sku, patch) => {
        const item = items.find((entry) => entry.productId === id && entry.skuId === sku)
        if (item) Object.assign(item, patch)
      } }, true, current)
      return current() && !items.some(membershipSalesBlocked)
    } catch {
      if (current()) failed.value = true
      console.warn('Checkout catalog verification failed')
      return false
    } finally {
      if (request === revision) checking.value = false
    }
  }
  return { blocked, checking, failed, refresh }
}
