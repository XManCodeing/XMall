export function parseNavExpansion(raw: string | null, legacy = false): Record<string, boolean> {
  if (!raw) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const entries = Object.entries(parsed).filter(([, value]) => typeof value === 'boolean')
    // 旧版会把所有分组的默认 true 持久化，视作旧默认值而非手动偏好。
    if (legacy && entries.length > 1 && entries.every(([, value]) => value === true)) return {}
    return Object.fromEntries(entries) as Record<string, boolean>
  } catch {
    return {}
  }
}

export function resolveNavExpansion(saved: boolean | undefined, active: boolean, searching: boolean): boolean {
  return searching || (saved ?? active)
}
