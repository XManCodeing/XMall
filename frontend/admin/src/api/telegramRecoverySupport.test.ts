import { describe, expect, it, vi } from 'vitest'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))
import { supportRecordedReceipt, supportDecisions, supportPage, supportEntry, supportOperation, validRecoverySupportResolution, telegramRecoverySupportAPI as api,
  type RecoverySupportEntry, type RecoverySupportOperation, type RecoverySupportResolution } from './telegramRecoverySupport'
const entry: RecoverySupportEntry = { id: '9007199254740993', target_id: '2', revision: '3', state: 'unknown', legacy: '1', operation_id: '9007199254740995' }
const operation: RecoverySupportOperation = { id: entry.operation_id, version: '9007199254740997', state: 'unknown', retired: false, target_id: '2', route_version: '3', chat_id: '-1002', action: 'create' }
const input: RecoverySupportResolution = { task_version: '9007199254740993', revision: '3', operation_id: operation.id, operation_version: operation.version, decision: 'succeeded', evidence: 'case:review-1', thread_id: '9007199254740999' }

describe('task-owned support contract', () => {
  it('uses exact bounded task/entry routes and string CAS', () => {
    api.entries('9', '9007199254740993'); api.operations('9', entry.id, '4'); api.resolve('9', entry.id, input)
    expect(mocks.get).toHaveBeenCalledWith('/admin/telegram-bot/recovery/tasks/9/support-entries', { params: { after_id: entry.id, limit: 20 } })
    expect(mocks.get).toHaveBeenCalledWith(`/admin/telegram-bot/recovery/tasks/9/support-entries/${entry.id}/operations`, { params: { after_id: '4', limit: 20 } })
    expect(mocks.post).toHaveBeenCalledWith(`/admin/telegram-bot/recovery/tasks/9/support-entries/${entry.id}/resolve`, input)
  })
  it('matches unknown, failed, retired and unrelated-operation rules', () => {
    expect(supportDecisions(entry, { ...operation, target_id: '99' })).toEqual([])
    expect(supportDecisions(entry, { ...operation, retired: true, action: 'delete' })).toEqual([])
    expect(supportDecisions({ ...entry, state: 'failed' }, { ...operation, state: 'failed', action: 'delete' })).toEqual([])
    expect(supportDecisions(entry, operation)).toEqual(['failed', 'succeeded'])
    expect(supportDecisions({ ...entry, state: 'failed' }, { ...operation, state: 'failed' })).toEqual(['failed', 'retry'])
    expect(supportDecisions(entry, { ...operation, id: '55' })).toEqual([])
    for (const state of ['unknown', 'creating']) expect(supportDecisions(entry, { ...operation, retired: true, state })).toEqual(['failed'])
    expect(supportDecisions({ ...entry, legacy: '0' }, { ...operation, retired: true })).toEqual([])
    expect(supportDecisions(entry, { ...operation, retired: true, state: 'succeeded' })).toEqual([])
    expect(supportDecisions({ ...entry, state: 'restored' }, operation)).toEqual([])
  })
  it.each(['succeeded', 'failed'] as const)('offers receipt-only %s review without converting another result', state => {
    const recordedEntry = { ...entry, state: state === 'succeeded' ? 'restored' : 'failed' }
    const recordedOperation = { ...operation, state }
    expect(supportRecordedReceipt(recordedEntry, recordedOperation)).toBe(true)
    expect(supportDecisions(recordedEntry, recordedOperation)).toEqual(state === 'succeeded' ? ['succeeded'] : ['failed', 'retry'])
    for (const change of [{ retired: true }, { action: 'delete' }, { action: '' }, { target_id: '99' }, { id: '99' }, { version: '1' }]) {
      expect(supportRecordedReceipt(recordedEntry, { ...recordedOperation, ...change })).toBe(false)
      expect(supportDecisions(recordedEntry, { ...recordedOperation, ...change })).not.toContain(state)
    }
    expect(supportRecordedReceipt({ ...recordedEntry, state: 'unknown' }, recordedOperation)).toBe(false)
    expect(supportDecisions({ ...recordedEntry, state: state === 'failed' ? 'restored' : 'failed' }, recordedOperation)).toEqual([])
  })
  it('rejects precision loss, malformed evidence and noncanonical thread IDs', () => {
    expect(validRecoverySupportResolution(input)).toBe(true)
    for (const thread_id of ['0', '-1', '01', '1e4', ' 1', '1 ', '9223372036854775808']) expect(validRecoverySupportResolution({ ...input, thread_id })).toBe(false)
    expect(validRecoverySupportResolution({ ...input, thread_id: '9223372036854775807' })).toBe(true)
    for (const evidence of ['case:', 'ticket:_bad', 'case:中文', 'case:has space', 'https://t.me/1', `case:${'a'.repeat(97)}`, 'case:ok\n']) expect(validRecoverySupportResolution({ ...input, evidence })).toBe(false)
    for (const field of ['task_version', 'revision', 'operation_version'] as const) {
      for (const value of ['0', '01', '9223372036854775807', 12]) expect(validRecoverySupportResolution({ ...input, [field]: value })).toBe(false)
    }
    expect(validRecoverySupportResolution({ ...input, decision: 'failed' })).toBe(false)
    expect(validRecoverySupportResolution({ ...input, decision: 'failed', thread_id: '0' })).toBe(true)
  })
  it('rejects malformed, oversized and backwards cursor pages', () => {
    const page = { task_version: '2', items: [entry], next_after_id: entry.id }
    expect(supportPage(page, '0', supportEntry)).toBe(true)
    for (const changed of [{ next_after_id: '1' }, { task_version: 2 }, { items: Array(21).fill(entry) }, { items: [{ ...entry, id: 3 }] }, { items: [entry, entry] }]) expect(supportPage({ ...page, ...changed }, '0', supportEntry)).toBe(false)
    expect(supportPage(page, entry.id, supportEntry)).toBe(false)
    expect(supportOperation({ ...operation, retired: 'false' })).toBe(false)
  })
})
