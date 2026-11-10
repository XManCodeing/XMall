import { productAPI } from '../api'
import type { CartItem } from '../stores/cart'
import { resolveCartPricingSnapshot } from './cartPricingSnapshot'
import { normalizeSkuId } from './sku'

interface CartStoreLike {
  items: CartItem[]
  patchItem: (productId: number, skuId: number | undefined, patch: Partial<CartItem>) => void
}

const normalizeStockNumber = (value: unknown) => {
  const numberValue = Number(value)
  if (!Number.isFinite(numberValue)) return 0
  return Math.max(Math.floor(numberValue), 0)
}

const normalizeManualStockTotal = (value: unknown) => {
  const numberValue = Number(value)
  if (!Number.isFinite(numberValue)) return 0
  const integerValue = Math.floor(numberValue)
  if (integerValue === -1) return -1
  return Math.max(integerValue, 0)
}

const normalizeSkuCode = (value: unknown) => String(value || '').trim().toUpperCase()

const normalizeOptionalLimitNumber = (value: unknown) => {
  const numberValue = Number(value)
  if (!Number.isFinite(numberValue)) return undefined
  const integerValue = Math.floor(numberValue)
  if (integerValue <= 0) return undefined
  return integerValue
}

// cartItemPurchaseLimit 返回购物车项的单次最大购买上限（无限制时返回 null）。
export const cartItemPurchaseLimit = (item: Pick<CartItem, 'maxPurchaseQuantity'>) =>
  normalizeOptionalLimitNumber(item.maxPurchaseQuantity) ?? null

// cartItemPurchaseMin 返回购物车项的单次最小购买下限（默认 1）。
export const cartItemPurchaseMin = (item: Pick<CartItem, 'minPurchaseQuantity'>) => {
  const minimum = normalizeOptionalLimitNumber(item.minPurchaseQuantity)
  return minimum && minimum > 0 ? minimum : 1
}

const resolveActiveSkus = (product: any) => {
  const rows = Array.isArray(product?.skus) ? product.skus : []
  return rows.filter((sku: any) => Boolean(sku?.is_active))
}

const resolveMatchedSku = (item: CartItem, activeSkus: any[]) => {
  const skuId = normalizeSkuId(item.skuId)
  if (skuId > 0) {
    const matchedById = activeSkus.find((sku: any) => normalizeSkuId(sku?.id) === skuId)
    if (matchedById) return matchedById
  }

  const skuCode = normalizeSkuCode(item.skuCode)
  if (skuCode) {
    const matchedByCode = activeSkus.find((sku: any) => normalizeSkuCode(sku?.sku_code) === skuCode)
    if (matchedByCode) return matchedByCode
  }

  if (activeSkus.length === 1) {
    return activeSkus[0]
  }

  return null
}

const shouldEnforceSkuStock = (product: any, sku: any) => {
  if (!product || !sku) return false
  if (sku?.stock_quantity_hidden === true || product?.stock_quantity_hidden === true) return false
  const type = String(product?.fulfillment_type || '').trim()
  if (type === 'auto') return true
  if (type === 'upstream') return true
  if (type !== 'manual') return false
  const total = normalizeManualStockTotal(sku?.manual_stock_total)
  if (total === -1) return false
  return true
}

/** 刷新目录快照；strict 用于离站恢复，商品/SKU 失效或请求失败时拒绝恢复，不替换用户选择。
 * isCurrent 在异步请求后校验调用方账号/页面仍有效；失效时不写入任何目录快照。
 * 返回刷新完成的 Promise；严格校验或上下文失效时抛错，由调用方展示既有错误状态。
 */
export const refreshCartStockSnapshots = async (cartStore: CartStoreLike, strict = false, isCurrent: () => boolean = () => true) => {
  const items = Array.isArray(cartStore.items) ? cartStore.items : []
  if (items.length === 0) return

  const slugSet = new Set<string>()
  for (const item of items) {
    const slug = String(item.slug || '').trim()
    if (!slug && strict) throw new Error('Purchase product is unavailable')
    if (slug) slugSet.add(slug)
  }
  if (slugSet.size === 0) return

  const productMap = new Map<string, any>()
  const requests = Array.from(slugSet).map(async (slug) => {
    try {
      const response = await productAPI.detail(slug)
      const product = response.data?.data
      if (product) {
        productMap.set(slug, product)
      }
    } catch {
      if (strict) console.warn('Purchase product refresh failed')
      // 常规购物车保留旧快照；严格恢复在下方拒绝缺失的目录响应。
    }
  })

  await Promise.all(requests)
  // 先验证所有条目再统一提交，避免严格恢复失败时只更新混合购物车的一部分。
  if (!isCurrent()) throw new Error('Purchase context changed')
  const pendingPatches: Array<{ item: CartItem; patch: Partial<CartItem> }> = []

  for (const item of items) {
    const slug = String(item.slug || '').trim()
    if (!slug) continue
    const product = productMap.get(slug)
    if (!product) {
      if (strict) throw new Error('Purchase product refresh failed')
      continue
    }
    if (strict && Number(product.id) !== item.productId) throw new Error('Purchase product changed')
    const activeSkus = resolveActiveSkus(product)
    const matchedSku = strict
      ? activeSkus.find((sku: any) => normalizeSkuId(sku.id) === item.skuId)
      : resolveMatchedSku(item, activeSkus)
    if (!matchedSku) {
      if (strict) throw new Error('Purchase SKU is unavailable')
      continue
    }

    const manualStockTotal = normalizeManualStockTotal(matchedSku?.manual_stock_total)
    const manualStockLocked = normalizeStockNumber(matchedSku?.manual_stock_locked)
    const manualStockSold = normalizeStockNumber(matchedSku?.manual_stock_sold)
    const autoStockAvailable = normalizeStockNumber(matchedSku?.auto_stock_available)
    const upstreamStock = normalizeManualStockTotal(matchedSku?.upstream_stock)
    const skuStockEnforced = shouldEnforceSkuStock(product, matchedSku)
    const stockRangeMin = normalizeStockNumber(matchedSku?.stock_range_min)
    const stockRangeMax = normalizeStockNumber(matchedSku?.stock_range_max)

    pendingPatches.push({ item, patch: {
      membership: product.membership,
      ...resolveCartPricingSnapshot(product, matchedSku),
      ...(strict ? {
        title: product.title,
        priceAmount: String(matchedSku.price_amount ?? ''),
        purchaseType: String(product.purchase_type || ''),
        paymentChannelIds: Array.isArray(product.payment_channel_ids) ? product.payment_channel_ids : undefined,
      } : {}),
      skuCode: String(matchedSku?.sku_code || item.skuCode || ''),
      skuSpecValues: (matchedSku?.spec_values && typeof matchedSku.spec_values === 'object')
        ? matchedSku.spec_values
        : item.skuSpecValues,
      skuManualStockTotal: manualStockTotal,
      skuManualStockLocked: manualStockLocked,
      skuManualStockSold: manualStockSold,
      skuAutoStockAvailable: autoStockAvailable,
      skuUpstreamStock: upstreamStock,
      skuStockStatus: String(matchedSku?.stock_status || ''),
      skuStockDisplayMode: String(matchedSku?.stock_display_mode || product?.stock_display_mode || ''),
      skuStockDisplay: String(matchedSku?.stock_display || ''),
      skuStockRangeMin: stockRangeMin > 0 ? stockRangeMin : undefined,
      skuStockRangeMax: stockRangeMax > 0 ? stockRangeMax : undefined,
      skuStockQuantityHidden: Boolean(matchedSku?.stock_quantity_hidden || product?.stock_quantity_hidden),
      skuStockEnforced,
      skuStockSnapshotAt: new Date().toISOString(),
      minPurchaseQuantity: normalizeOptionalLimitNumber(product?.min_purchase_quantity),
      maxPurchaseQuantity: normalizeOptionalLimitNumber(product?.max_purchase_quantity),
      fulfillmentType: String(product?.fulfillment_type || item.fulfillmentType || ''),
      manualFormSchema: product?.manual_form_schema || {},
    } })
  }
  if (!isCurrent()) throw new Error('Purchase context changed')
  for (const { item, patch } of pendingPatches) {
    cartStore.patchItem(item.productId, item.skuId, patch)
  }
}
