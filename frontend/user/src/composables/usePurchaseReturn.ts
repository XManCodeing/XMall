import { useRouter } from 'vue-router'
import { productAPI, userProfileAPI } from '../api'
import { useUserAuthStore } from '../stores/userAuth'
import { useBuyNowStore } from '../stores/buyNow'
import { useCartStore } from '../stores/cart'
import { refreshCartStockSnapshots } from '../utils/cartStock'
import { readPurchaseContext } from '../utils/purchaseContext'

/** 绑定成功后按当前服务端账号恢复。返回 false 表示没有可恢复上下文；不创建订单或支付。 */
export function usePurchaseReturn() {
  const router = useRouter()
  const auth = useUserAuthStore()

  /** 读取一次绑定返回上下文；身份/商品失败抛出，由既有绑定错误反馈承接。 */
  const finishPurchaseReturn = async (): Promise<boolean> => {
    const accountId = Number(auth.user?.id)
    if (!auth.isAuthenticated) return false
    const isCurrent = () => auth.isAuthenticated && Number(auth.user?.id) === accountId
    const context = readPurchaseContext(accountId, undefined, true)
    if (!context) return false
    const [profile, binding] = await Promise.all([
      userProfileAPI.current(), userProfileAPI.getTelegramBinding(),
    ])
    if (Number(profile.data.data?.id) !== accountId || !binding.data.data?.bound
      || !auth.isAuthenticated || Number(auth.user?.id) !== accountId) {
      throw new Error('Purchase return identity verification failed')
    }
    if (context.returnPath === '/checkout?mode=buynow') {
      const buyNow = useBuyNowStore()
      // 内存和持久化金额都不作为恢复依据；始终刷新现有 SKU。
      await buyNow.restore()
      const current = buyNow.item
      const saved = context.items[0]
      if (!current || !saved || current.productId !== saved.productId || current.skuId !== saved.skuId
        || current.quantity !== saved.quantity) throw new Error('Purchase selection changed')
      await refreshCartStockSnapshots({ items: [current], patchItem: (_id, _sku, patch) => {
        Object.assign(current, patch)
      } }, true, () => isCurrent() && buyNow.item === current)
    } else if (context.returnPath.startsWith('/products/')) {
      const saved = context.items[0]
      if (!saved) return false
      const response = await productAPI.detail(saved.slug)
      const product = response.data.data
      if (Number(product?.id) !== saved.productId || !product?.skus?.some((sku: any) => sku.is_active && Number(sku.id) === saved.skuId)) {
        throw new Error('Purchase selection is unavailable')
      }
    } else {
      // 保留现有混合购物车与离站期间修改，不用旧快照覆盖或删除任何条目。
      await refreshCartStockSnapshots(useCartStore(), true, isCurrent)
    }
    if (!auth.isAuthenticated || Number(auth.user?.id) !== accountId) return false
    const selection = context.items[0]
    await router.replace(context.returnPath.startsWith('/products/') && selection
      ? { path: context.returnPath, query: { sku_id: selection.skuId } }
      : context.returnPath)
    return true
  }
  return { finishPurchaseReturn }
}
