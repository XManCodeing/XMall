export const resellerOperationsFinancePermission = 'GET:/admin/resellers/operations/finance'

export const hasFinancePermission = (hasPermission: (permission: string) => boolean) =>
  hasPermission(resellerOperationsFinancePermission)

export const buildResellerOperationsAlertClass = (level?: string) => {
  if (level === 'warning') return 'border-amber-200 bg-amber-50 text-amber-800'
  if (level === 'danger') return 'border-rose-200 bg-rose-50 text-rose-800'
  if (level === 'info') return 'border-sky-200 bg-sky-50 text-sky-800'
  return 'border-border bg-muted/30 text-muted-foreground'
}

export const normalizeCurrencyRows = <T extends { currency?: string }>(rows?: T[] | null) =>
  Array.isArray(rows)
    ? rows.map((row) => ({ ...row, currency: String(row.currency || '').trim().toUpperCase() }))
    : []

import { formatDate as formatPeriodBoundary } from '../../../shared/dateTime.ts'

export const formatResellerOperationsPeriod = (from?: string, to?: string) =>
  `${formatPeriodBoundary(from)} - ${formatPeriodBoundary(to)}`
