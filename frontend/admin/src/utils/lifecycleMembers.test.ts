import { describe, expect, it } from 'vitest'
import { compareLifecycleIDs, lifecycleMemberViews } from './lifecycleMembers'
import type { MembershipLifecycleTask as Task } from '@/api/telegramMembership'

/** Build explicit group evidence. @param input Test overrides. @returns A persisted task fixture. */
function task(input: Partial<Task>): Task {
 return { id: '1', telegram_id: '9007199254740993', chat_id: '-1009007199254740993', target_id: 'not-visible', version: '1', state: 'succeeded', phase: 'done', review_reason: 'removed', ban_outcome: 'succeeded', unban_outcome: 'succeeded', ban_blocked: false, ...input }
}
describe('lifecycle member projection', () => {
 /** 构造同周期的成功移出及后续无观测扫描。@returns 两条真实字段结构的测试任务。 */
 function disposition(): [Task, Task] {
  const scope = { cleanup_cycle_id: '172738', entitlement_version: '32', route_version: '4', bot_id: '1', target_id: '1' }
  return [
   task({ ...scope, id: '172752', ban_confirmed: true, member_outcome: 'succeeded', observed_at: '2026-11-07T14:50:15Z', member_is_member: false, invite_cleanup: 'succeeded' }),
   task({ ...scope, id: '172755', phase: 'not_required', member_outcome: 'not_required', review_reason: '', ban_confirmed: false, ban_outcome: '', unban_outcome: '', observed_at: '0001-01-01T00:00:00Z', invite_cleanup: 'failed' }),
  ]
 }
 it('retains confirmed removal within one disposition without hiding the latest cleanup result', () => {
  const [removed, noop] = disposition()
  const [member] = lifecycleMemberViews([removed, noop])
  expect(member?.memberEvidence).toEqual([removed])
  expect(member?.latest).toEqual([noop])
  expect(member?.latest[0]?.invite_cleanup).toBe('failed')
  expect(member?.tasks).toEqual([noop, removed])
 })
 it.each(['cleanup_cycle_id', 'entitlement_version', 'route_version', 'bot_id', 'target_id'] as const)('never borrows evidence across a different or missing %s', field => {
  const [removed, noop] = disposition()
  for (const value of ['999', undefined, '0']) {
   const latest = { ...noop, [field]: value }
   expect(lifecycleMemberViews([removed, latest as Task])[0]?.memberEvidence).toEqual([latest])
  }
 })
 it.each([
  { observed_at: '2026-11-07T14:50:18Z', member_is_member: true },
  { state: 'needs_review' }, { phase: 'unknown_ban' }, { review_reason: 'entitlement_active' },
  { review_reason: 'admission_restored' }, { ban_blocked: true }, { ban_outcome: 'failed' },
 ])('preserves newer observations or unresolved work: %j', changes => {
  const [removed, noop] = disposition()
  const latest = { ...noop, ...changes }
  expect(lifecycleMemberViews([removed, latest])[0]?.memberEvidence).toEqual([latest])
 })
 it('does not skip an intervening observation or borrow from another member or group', () => {
  const [removed, noop] = disposition()
  const observed = { ...noop, id: '172754', observed_at: '2026-11-07T14:50:17Z' }
  expect(lifecycleMemberViews([removed, observed, noop])[0]?.memberEvidence).toEqual([noop])
  expect(lifecycleMemberViews([{ ...removed, telegram_id: '42' }, noop])[1]?.memberEvidence).toEqual([noop])
  expect(lifecycleMemberViews([{ ...removed, chat_id: '-1002' }, noop])[0]?.memberEvidence[0]).toEqual(noop)
 })
 it('sorts identifiers beyond safe integers by numeric string length and value', () => {
  expect(compareLifecycleIDs('9007199254740993', '9007199254740992')).toBeGreaterThan(0)
  expect(compareLifecycleIDs('10000000000000000000', '9999999999999999999')).toBeGreaterThan(0)
  expect(compareLifecycleIDs('0009', '9')).toBe(0)
 })
 it('counts unique chats using their latest task while retaining unfinished history', () => {
  const rows = lifecycleMemberViews([
   task({ id: '9007199254740992', state: 'needs_review' }),
   task({ id: '9007199254740993' }),
   task({ id: '9007199254740994', chat_id: '-1002', state: 'pending', invite_cleanup: 'failed' }),
   task({ id: '9007199254740995', telegram_id: '9007199254740992', chat_id: '-1003' }),
  ])
  expect(rows).toHaveLength(2)
  expect(rows[0]?.chatIds).toEqual(['-1002', '-1009007199254740993'])
  expect(rows[0]?.latest.map(row => row.id)).toEqual(['9007199254740994', '9007199254740993'])
  expect(rows[0]?.tasks).toHaveLength(3)
  expect(rows[0]?.historyPending).toBe(1)
  expect(rows[1]?.telegram_id).toBe('9007199254740992')
 })
})
