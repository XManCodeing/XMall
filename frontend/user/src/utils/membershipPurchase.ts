/** Public membership data only; no internal group routing or entitlement state is exposed. */
export interface MembershipPurchase {
  config_version: number
  quantity: 1
  sales_open: boolean
  group_names: string[]
  terms: Array<{ sku_id: number; sku_code: string; kind: 'months' | 'permanent'; months: number }>
}

/** Identify membership by projection presence, never by the login-only purchase_type. */
export function isMembershipProduct(product: { membership?: unknown } | null | undefined): boolean {
  return product?.membership !== undefined && product?.membership !== null
}

/**
 * Read only public group names, preserving their configured order without interpreting HTML.
 * @param product Latest public catalog projection; internal targets are never used as a fallback.
 * @returns Valid display names, or an empty list for an absent/malformed projection.
 */
export function membershipGroupNames(product: { membership?: unknown } | null | undefined): string[] {
  if (!isMembershipProduct(product) || typeof product?.membership !== 'object') return []
  const names = (product.membership as Partial<MembershipPurchase>).group_names
  if (!Array.isArray(names) || names.some((name) => typeof name !== 'string' || !name.trim())) return []
  return names.map((name) => name.trim())
}

/** Resolve the selected active SKU's public term. DEFAULT is an ordinary SKU code. */
export function membershipTerm(product: { membership?: unknown } | null | undefined, skuId: unknown) {
  if (!isMembershipProduct(product)) return null
  const projection = product!.membership as MembershipPurchase
  if (!Array.isArray(projection?.terms)) return null
  const term = projection.terms.find((entry) => entry.sku_id === Number(skuId))
  if (!term || !Number.isSafeInteger(term.sku_id) || term.sku_id <= 0) return null
  if (term.kind === 'permanent') return term
  return term.kind === 'months' && Number.isSafeInteger(term.months) && term.months > 0 ? term : null
}

/**
 * 仅接受服务端明确放行的会员投影；缺失或异常状态保持停售。
 * @param product 最新公开商品投影，普通商品不受会员门控影响。
 * @returns 是否应阻止会员购买；最终授权仍由下单与支付事务执行。
 */
export function membershipSalesBlocked(product: { membership?: unknown } | null | undefined): boolean {
  if (!isMembershipProduct(product)) return false
  const projection = product?.membership
  return typeof projection !== 'object' || projection === null ||
    (projection as Partial<MembershipPurchase>).sales_open !== true
}

/** Format a validated term via the caller's translator; a month always means 30 days. */
export function membershipTermLabel(product: { membership?: unknown } | null | undefined, skuId: unknown,
  translate: (key: string, values?: Record<string, number>) => string): string {
  const term = membershipTerm(product, skuId)
  if (!term) return translate('membershipPurchase.selectPlan')
  return term.kind === 'permanent' ? translate('membershipPurchase.permanent')
    : translate('membershipPurchase.months', { months: term.months, days: term.months * 30 })
}
