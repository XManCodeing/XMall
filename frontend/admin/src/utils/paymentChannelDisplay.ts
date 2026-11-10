type PaymentChannelConfig = Record<string, unknown> | null | undefined

/**
 * Normalize product payment channel IDs for both general and membership editors.
 * @param raw Serialized response IDs or an editable numeric array.
 * @returns Positive finite IDs; malformed legacy values fall back to an empty selection.
 */
export const parsePaymentChannelIDs = (raw: string | number[] | null | undefined): number[] => {
  if (!raw) return []
  if (Array.isArray(raw)) return raw.filter((id) => Number.isFinite(id) && id > 0)
  const trimmed = raw.trim()
  if (!trimmed || trimmed === '[]') return []
  try {
    const parsed: unknown = JSON.parse(trimmed)
    return Array.isArray(parsed) ? parsed.filter((id: unknown): id is number => typeof id === 'number' && Number.isFinite(id) && id > 0) : []
  } catch {
    // Match the existing general editor's safe fallback without logging product data.
    return []
  }
}

const readConfigString = (config: PaymentChannelConfig, key: string) => {
  const value = config?.[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value).trim() : ''
}

export const resolveOkpayConfiguredCoin = (config: PaymentChannelConfig) => {
  return readConfigString(config, 'coin').toUpperCase()
}

export const resolveOkpayChannelTypeFromConfig = (config: PaymentChannelConfig) => {
  switch (resolveOkpayConfiguredCoin(config).toLowerCase()) {
    case 'usdt':
      return 'usdt'
    case 'trx':
      return 'trx'
    default:
      return ''
  }
}
