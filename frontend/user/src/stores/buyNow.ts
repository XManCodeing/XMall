import { defineStore } from 'pinia'
import { ref, computed, watch } from 'vue'
import type { CartItem } from './cart'
import { useUserAuthStore } from './userAuth'
import { refreshCartStockSnapshots } from '../utils/cartStock'
import { userProfileAPI } from '../api'
import { BUY_NOW_CONTEXT_KEY, clearPurchaseContext, purchaseSelection, readPurchaseContext, savePurchaseContext } from '../utils/purchaseContext'

/** 立即购买仅持久化登录账号的最小选择；恢复必须重新请求目录，不保存金额及人工输入。 */
export const useBuyNowStore = defineStore('buyNow', () => {
    const auth = useUserAuthStore()
    const item = ref<CartItem | null>(null)
    const hasItem = computed(() => item.value !== null)
    let revision = 0

    /** 保存当前条目；newItem 来自现有详情/快捷购买，返回值为空。 */
    const setItem = (newItem: CartItem) => {
        revision += 1
        item.value = { ...newItem }
        clearPurchaseContext(BUY_NOW_CONTEXT_KEY)
        const selection = purchaseSelection(newItem)
        if (selection && auth.isAuthenticated) {
            savePurchaseContext({ version: 1, accountId: Number(auth.user?.id), issuedAt: Date.now(),
                returnPath: '/checkout?mode=buynow', items: [selection] }, BUY_NOW_CONTEXT_KEY)
        }
    }

    /** 清除内存及持久选择；不修改普通购物车。 */
    const clear = () => {
        revision += 1
        item.value = null
        clearPurchaseContext(BUY_NOW_CONTEXT_KEY)
    }

    /** 从服务端重建立即购买条目；失败抛出供结算页展示，竞态响应不覆盖新选择。 */
    const restore = async (): Promise<boolean> => {
        if (item.value) return true
        const accountId = Number(auth.user?.id)
        if (!auth.isAuthenticated) return false
        const context = readPurchaseContext(accountId, BUY_NOW_CONTEXT_KEY)
        const selection = context?.items[0]
        if (!selection) return false
        const currentRevision = revision
        const profile = await userProfileAPI.current()
        if (Number(profile.data.data?.id) !== accountId) throw new Error('Purchase account changed')
        const restored: CartItem = { ...selection, title: {}, priceAmount: '' }
        await refreshCartStockSnapshots({ items: [restored], patchItem: (_productId, _skuId, patch) => {
            Object.assign(restored, patch)
        } }, true)
        if (currentRevision !== revision || Number(auth.user?.id) !== accountId || !auth.isAuthenticated) return false
        if (!readPurchaseContext(accountId, BUY_NOW_CONTEXT_KEY)) return false
        item.value = restored
        return true
    }

    watch(() => [auth.user?.id, auth.isAuthenticated], clear, { flush: 'sync' })
    return { item, hasItem, setItem, clear, restore }
})
