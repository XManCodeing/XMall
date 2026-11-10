import { beforeEach, describe, expect, it, vi } from 'vitest'
import { telegramSupportAPI, validSupportResolution, validSupportTopicResolution, validSupportThread, type SupportResolution, type SupportTopicResolution } from './telegramSupport'
const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('./client', () => ({ api: mocks }))
beforeEach(() => vi.clearAllMocks())
describe('support API contract', () => {
  it('uses existing metadata and explicit content endpoints', () => {
    const filter = { page: 2, page_size: 20, bot_id: 3, state: 'unknown' }
    telegramSupportAPI.sessions(filter); telegramSupportAPI.session(4); telegramSupportAPI.messages(4, filter)
    telegramSupportAPI.content(4, 5); telegramSupportAPI.tasks(filter); telegramSupportAPI.task(6)
    expect(mocks.get.mock.calls).toEqual([
      ['/admin/telegram-bot/support/sessions', { params: filter }], ['/admin/telegram-bot/support/sessions/4'],
      ['/admin/telegram-bot/support/sessions/4/messages', { params: filter }], ['/admin/telegram-bot/support/sessions/4/messages/5/content'],
      ['/admin/telegram-bot/support/downstreams', { params: filter }], ['/admin/telegram-bot/support/downstreams/6'],
    ])
  })
  it('whitelists session operation filters including explicit false without task leakage', () => {
    const filter = { page: 1, page_size: 20, unreplied: true, blocked: false, has_topic: true, failure: 'upstream' as const, actor_id: 7, body: 'private' }
    telegramSupportAPI.sessions(filter)
    expect(mocks.get).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/support/sessions', { params: { page: 1, page_size: 20, bot_id: undefined, state: undefined, unreplied: true, blocked: false, has_topic: true, failure: 'upstream' } })
    expect(mocks.post).not.toHaveBeenCalled()
  })
  it('projects only four authorized input fields, never trusted routing or receipt fields', () => {
    const input = { expected_version: 3, decision: 'delivered', evidence: 'ticket:review-1', delivery_id: 9, generation: 2, actor_id: 8, message_ids: [1] } as SupportResolution
    telegramSupportAPI.resolve(6, input)
    expect(mocks.post).toHaveBeenCalledExactlyOnceWith('/admin/telegram-bot/support/downstreams/6/resolve', { expected_version: 3, decision: 'delivered', evidence: 'ticket:review-1', delivery_id: 9 })
  })
  it('keeps independent resource paths and whitelists query and resolver fields', () => {
    const filter = { page: 2, page_size: 20, bot_id: 3, state: 'unknown', actor_id: 8, generation: 2, content: 'private' }
    telegramSupportAPI.upstreams(filter); telegramSupportAPI.topics(filter)
    telegramSupportAPI.upstream(6); telegramSupportAPI.topic(6)
    expect(mocks.get.mock.calls).toEqual([
      ['/admin/telegram-bot/support/upstreams', { params: { page: 2, page_size: 20, bot_id: 3, state: 'unknown' } }],
      ['/admin/telegram-bot/support/topic-operations', { params: { page: 2, page_size: 20, bot_id: 3, state: 'unknown' } }],
      ['/admin/telegram-bot/support/upstreams/6'], ['/admin/telegram-bot/support/topic-operations/6'],
    ])
    const upstream = { expected_version: 3, decision: 'delivered', evidence: 'ticket:review', delivery_id: 9, actor_id: 1, generation: 2 } as SupportResolution
    const topic = { expected_version: 4, decision: 'succeeded', evidence: 'case:review', thread_id: '9223372036854775807', no_effect: false, actor_id: 1, generation: 2, delivery_id: 9 } as SupportTopicResolution
    telegramSupportAPI.resolveUpstream(6, upstream); telegramSupportAPI.resolveTopic(6, topic)
    expect(mocks.post.mock.calls).toEqual([
      ['/admin/telegram-bot/support/upstreams/6/resolve', { expected_version: 3, decision: 'delivered', evidence: 'ticket:review', delivery_id: 9 }],
      ['/admin/telegram-bot/support/topic-operations/6/resolve', { expected_version: 4, decision: 'succeeded', evidence: 'case:review', thread_id: '9223372036854775807', no_effect: false }],
    ])
  })
  it('validates full int64 strings and explicit no-effect failures without rounding', () => {
    for (const thread of ['1', '9007199254740993', '9223372036854775807']) expect(validSupportThread(thread)).toBe(true)
    for (const thread of ['', '0', '-1', '+1', '01', '1.1', '1e3', ' 1', '1\n', '9223372036854775808', '99999999999999999999']) expect(validSupportThread(thread)).toBe(false)
    const input: SupportTopicResolution = { expected_version: 3, decision: 'succeeded', evidence: 'ticket:verified', thread_id: '9007199254740993', no_effect: false }
    expect(validSupportTopicResolution(input)).toBe(true)
    expect(validSupportTopicResolution({ ...input, no_effect: true })).toBe(false)
    expect(validSupportTopicResolution({ ...input, expected_version: 0 })).toBe(false)
    for (const evidence of ['https://example.com', 'ticket:', 'case:_invalid', 'audit:private text', 'case:review\n', `ticket:${'x'.repeat(97)}`]) expect(validSupportTopicResolution({ ...input, evidence })).toBe(false)
    const failed: SupportTopicResolution = { ...input, decision: 'failed', thread_id: '', no_effect: true }
    expect(validSupportTopicResolution(failed)).toBe(true)
    expect(validSupportTopicResolution({ ...failed, thread_id: '0' })).toBe(true)
    expect(validSupportTopicResolution({ ...failed, thread_id: '1' })).toBe(false)
    expect(validSupportTopicResolution({ ...failed, no_effect: false })).toBe(false)
    telegramSupportAPI.resolveTopic(6, failed)
    expect(mocks.post.mock.calls[0]?.[1]).toEqual({ ...failed, thread_id: '0' })
  })
  it('rejects arbitrary evidence, unsafe versions, unsupported decisions and missing local proof', () => {
    const input: SupportResolution = { expected_version: 3, decision: 'failed', evidence: 'audit:review_1', delivery_id: 9 }
    expect(validSupportResolution(input)).toBe(true)
    for (const evidence of ['https://example.com', 'ticket:', 'ticket:_bad', 'ticket:contains secret', `case:${'x'.repeat(97)}`]) expect(validSupportResolution({ ...input, evidence })).toBe(false)
    expect(validSupportResolution({ ...input, expected_version: Number.MAX_SAFE_INTEGER + 1 })).toBe(false)
    expect(validSupportResolution({ ...input, delivery_id: 0 })).toBe(false)
  })
})
