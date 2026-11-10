/** 已购买的会员快照；不代表当前权益或开通状态。 */
export interface MembershipOrderSnapshot {
  beneficiary_telegram_id: string
  config_version: number
  kind: 'months' | 'permanent'
  months: number
  quantity: 1
}

/**
 * 校验订单接口中的购买快照，保留 Telegram ID 字符串精度。
 * @param value 订单项携带的原始快照。
 * @returns 可展示的快照；普通商品或不完整数据返回 null。
 */
export function membershipOrderSnapshot(value: unknown): MembershipOrderSnapshot | null {
  if (!value || typeof value !== 'object') return null
  const snapshot = value as Partial<MembershipOrderSnapshot>
  if (typeof snapshot.beneficiary_telegram_id !== 'string' || !/^[1-9]\d*$/.test(snapshot.beneficiary_telegram_id)) return null
  if (snapshot.quantity !== 1) return null
  if (snapshot.kind !== 'months' && snapshot.kind !== 'permanent') return null
  if (snapshot.kind === 'months' && (!Number.isSafeInteger(snapshot.months) || Number(snapshot.months) <= 0)) return null
  return snapshot as MembershipOrderSnapshot
}

/**
 * 格式化购买期限，只使用快照，不推算到期日期。
 * @param snapshot 已校验的订单购买快照。
 * @param translate 当前语言的翻译函数。
 * @returns 固定 30 天/月或永久的购买期限文案。
 */
export function membershipOrderDuration(
  snapshot: MembershipOrderSnapshot,
  translate: (key: string, values?: Record<string, number>) => string,
): string {
  return snapshot.kind === 'permanent'
    ? translate('membershipOrder.permanent')
    : translate('membershipOrder.months', { months: snapshot.months, days: snapshot.months * 30 })
}
