import { api } from './client'
import { recoveryBase, validRecoveryBotID, validRecoveryChatID } from './telegramRecovery'

export const supportEntriesPermission = `GET:${recoveryBase}/tasks/:id/support-entries`
export const supportOperationsPermission = `GET:${recoveryBase}/tasks/:id/support-entries/:entry/operations`
export const supportResolutionPermission = `POST:${recoveryBase}/tasks/:id/support-entries/:entry/resolve`
export const supportResumePermission = `POST:${recoveryBase}/tasks/:id/resume`
export interface RecoverySupportEntry {
  id: string
  target_id: string
  revision: string
  state: string
  legacy: string
  operation_id: string
}
export interface RecoverySupportOperation {
  id: string; version: string; state: string; retired: boolean
  target_id: string; route_version: string; chat_id: string; action: string
}
export interface RecoverySupportPage<T> { task_version: string; items: T[]; next_after_id: string }
export type RecoverySupportDecision = 'retry' | 'failed' | 'succeeded'
export interface RecoverySupportResolution {
  task_version: string
  revision: string
  operation_id: string
  operation_version: string
  decision: RecoverySupportDecision
  evidence: string
  thread_id: string
}

/** Validate a server CAS value without precision loss. @param value Wire value. @returns Positive signed-counter range. */
export function supportVersion(value: unknown): value is string {
  return typeof value === 'string' && value === value.trim() && /^[1-9]\d{0,18}$/.test(value) && BigInt(value) < 9223372036854775807n
}
/** Validate uint64 decimal strings, optionally zero. @param value Wire value. @returns Canonical unsigned ID. */
export function supportID(value: unknown, zero = false): value is string {
  return typeof value === 'string' && value === value.trim() && ((zero && value === '0') || validRecoveryBotID(value))
}
/** Validate a safe entry projection. @param value Untrusted response item. @returns Whether all DTO fields match. */
export function supportEntry(value: unknown): value is RecoverySupportEntry {
  const item = value as RecoverySupportEntry | null
  return !!item && supportID(item.id) && supportID(item.target_id) && supportVersion(item.revision) && typeof item.state === 'string'
    && supportID(item.legacy, true) && supportID(item.operation_id, true)
}
/** Validate a safe operation projection. @param value Untrusted response item. @returns Whether all DTO fields match. */
export function supportOperation(value: unknown): value is RecoverySupportOperation {
  const item = value as RecoverySupportOperation | null
  return !!item && supportID(item.id) && supportVersion(item.version) && typeof item.state === 'string' && typeof item.retired === 'boolean'
    && supportID(item.target_id) && supportID(item.route_version, true) && typeof item.action === 'string'
    && typeof item.chat_id === 'string' && item.chat_id === item.chat_id.trim() && validRecoveryChatID(item.chat_id)
}
/** Check one bounded ordered page and its forward-only cursor. @param value Response. @param after Requested cursor. @param check Item validator. @returns Valid page. */
export function supportPage<T extends { id: string }>(value: unknown, after: string, check: (item: unknown) => item is T): value is RecoverySupportPage<T> {
  const page = value as RecoverySupportPage<T> | null
  if (!page || !supportVersion(page.task_version) || !supportID(page.next_after_id, true) || !Array.isArray(page.items) || page.items.length > 20) return false
  let previous = after
  for (const item of page.items) {
    if (!check(item) || BigInt(item.id) <= BigInt(previous)) return false
    previous = item.id
  }
  return page.next_after_id === '0' || (page.items.length > 0 && page.next_after_id === previous)
}
/** Identify a candidate persisted result, not authorization to recover a checkpoint.
 * @param entry Owned entry. @param operation Listed operation. @returns Whether receipt-only review can be offered; binding and receipts remain server-verified.
 */
export function supportRecordedReceipt(entry: RecoverySupportEntry, operation: RecoverySupportOperation): boolean {
  return !operation.retired && operation.action === 'create' && operation.target_id === entry.target_id && entry.operation_id === operation.id
    && supportVersion(operation.version) && BigInt(operation.version) >= 2n
    && ((operation.state === 'succeeded' && entry.state === 'restored') || (operation.state === 'failed' && entry.state === 'failed'))
}
/** Mirror only decisions supported by the disclosed server state. Binding, receipt and authority checks remain server-owned.
 * @param entry Owned checkpoint entry. @param operation Owned operation. @returns Narrow permitted intents, never parent completion.
 */
export function supportDecisions(entry: RecoverySupportEntry, operation: RecoverySupportOperation): RecoverySupportDecision[] {
  if (operation.target_id !== entry.target_id) return []
  if (operation.retired) return ['', 'create'].includes(operation.action) && entry.legacy !== '0' && ['creating', 'unknown'].includes(operation.state) ? ['failed'] : []
  if (entry.operation_id !== operation.id) return []
  if (supportRecordedReceipt(entry, operation)) return operation.state === 'succeeded' ? ['succeeded'] : ['failed', 'retry']
  if (entry.state === 'failed' && operation.state === 'failed' && operation.action === 'create') return ['retry']
  return entry.state === 'unknown' && operation.state === 'unknown' ? ['failed', 'succeeded'] : []
}
/** Validate exact audit reference, CAS and conditional positive int64 topic ID. @param input Intent. @returns Strict validity without trimming. */
export function validRecoverySupportResolution(input: RecoverySupportResolution): boolean {
  if (!supportVersion(input.task_version) || !supportVersion(input.revision) || !supportID(input.operation_id) || !supportVersion(input.operation_version)) return false
  if (typeof input.evidence !== 'string' || input.evidence !== input.evidence.trim() || !/^(ticket|audit|case):[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/.test(input.evidence)) return false
  if (input.decision === 'succeeded') return typeof input.thread_id === 'string' && input.thread_id === input.thread_id.trim() && /^[1-9]\d{0,18}$/.test(input.thread_id) && BigInt(input.thread_id) <= 9223372036854775807n
  return ['failed', 'retry'].includes(input.decision) && input.thread_id === '0'
}

export const telegramRecoverySupportAPI = {
  /** Read one task-owned page. @param task Task ID. @param after Exclusive cursor. @returns Bounded entries response. */
  entries: (task: string, after = '0') => api.get(`${recoveryBase}/tasks/${task}/support-entries`, { params: { after_id: after, limit: 20 } }),
  /** Read one entry-owned page. @param task Task ID. @param entry Entry ID. @param after Cursor. @returns Operations response. */
  operations: (task: string, entry: string, after = '0') => api.get(`${recoveryBase}/tasks/${task}/support-entries/${entry}/operations`, { params: { after_id: after, limit: 20 } }),
  /** Save audited CAS only; never execute Telegram or complete parent. @param task Task ID. @param entry Entry ID. @param input Exact intent. @returns Resolution receipt. */
  resolve: (task: string, entry: string, input: RecoverySupportResolution) => api.post(`${recoveryBase}/tasks/${task}/support-entries/${entry}/resolve`, input),
}
