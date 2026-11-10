import type { TopicMappingSnapshot } from './telegramSupportTopic'
import { api } from './client'

export const supportBase = '/admin/telegram-bot/support'
export const supportContentPermission = `GET:${supportBase}/sessions/:id/messages/:message_id/content`
export const supportResolvePermission = `POST:${supportBase}/downstreams/:id/resolve`
export interface SupportFilter { page: number; page_size: number; bot_id?: number; state?: string }
export interface SupportSessionFilter extends SupportFilter { unreplied?: boolean; blocked?: boolean; has_topic?: boolean; failure?: 'any' | 'upstream' | 'downstream' | 'none' }
export interface SupportSession {
  id: number; bot_id: number; user_id: string; chat_id: string; state: string; version: number
  created_at: string; updated_at: string
  unreplied: boolean; unreplied_since: string | null; last_customer_delivered_at: string | null; last_reply_delivered_at: string | null
  blocked: boolean; block_expires_at: string | null
  upstream_failed: number; upstream_unknown: number; downstream_failed: number; downstream_unknown: number
  topic_chat_id: string; topic_thread_id: string; topic_state: string; topic_url: string
}
export interface SupportMessage {
  id: number; session_id: number; bot_id: number; sequence: number; state: string
  retain_until: string; created_at: string; updated_at: string
}
export interface SupportContent { available: boolean; reason?: string; kind?: string; text?: string; caption?: string; media_kinds?: string[] }
export interface SupportTask {
  id: number; bot_id: number; session_id: number; kind: string; state: string; version: number
  delivery_id: number; created_at: string; updated_at: string
}
export type SupportTaskResource = 'downstreams' | 'upstreams' | 'topic-operations'
export const supportTaskStates: Record<SupportTaskResource, readonly string[]> = {
  downstreams: ['unknown', 'pending', 'collecting', 'sending', 'delivered', 'failed', 'internal', 'cancelled'],
  upstreams: ['unknown', 'buffered', 'ready', 'claimed', 'sending', 'delivered', 'failed', 'expired', 'rejected', 'cancelled'],
  'topic-operations': ['unknown', 'creating', 'succeeded', 'failed'],
}
export interface SupportUpstream extends Omit<SupportTask, 'kind'> {}
export interface SupportTopic extends Omit<SupportTask, 'kind' | 'delivery_id'> { target_id: number; route_version: number; action: string; operation_version?: string; mapping?: TopicMappingSnapshot }
export type SupportTaskRecord = SupportTask | SupportUpstream | SupportTopic
export interface SupportTopicResolution {
  expected_version: number; decision: 'succeeded' | 'failed'; evidence: string; thread_id: string; no_effect: boolean
}
export interface SupportResolution { expected_version: number; decision: 'delivered' | 'failed'; evidence: string; delivery_id: number }

/** Validate a local positive identifier/version without silently rounding server uint64 values. */
export function supportPositive(value: number): boolean { return Number.isSafeInteger(value) && value > 0 }
/** Validate the four-field resolver contract; evidence is a reference, never message text. */
export function validSupportResolution(input: SupportResolution): boolean {
  return supportPositive(input.expected_version) && supportPositive(input.delivery_id)
    && ['delivered', 'failed'].includes(input.decision)
    && validSupportEvidence(input.evidence)
}
/** Validate evidence references only; free-form content and links are forbidden. */
export function validSupportEvidence(value: string): boolean { return value === value.trim() && /^(ticket|audit|case):[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/.test(value) }
/** Validate a decimal positive int64 without converting Telegram identifiers to Number. */
export function validSupportThread(value: string): boolean {
  return value === value.trim() && /^[1-9]\d{0,18}$/.test(value) && BigInt(value) <= BigInt('9223372036854775807')
}
/** Validate explicit observed success or no-effect failure against the strict topic DTO. */
export function validSupportTopicResolution(input: SupportTopicResolution): boolean {
  if (!supportPositive(input.expected_version) || !validSupportEvidence(input.evidence)) return false
  return input.decision === 'succeeded' ? validSupportThread(input.thread_id) && input.no_effect === false
    : input.decision === 'failed' && input.no_effect === true && (input.thread_id === '' || input.thread_id === '0')
}
/** Whitelist query metadata rather than forwarding caller-owned objects. */
function taskFilter(params: SupportFilter): SupportFilter {
  return { page: params.page, page_size: params.page_size, bot_id: params.bot_id, state: params.state }
}
export const telegramSupportAPI = {
  sessions: (params: SupportSessionFilter) => api.get(`${supportBase}/sessions`, { params: {
    ...taskFilter(params), unreplied: params.unreplied, blocked: params.blocked, has_topic: params.has_topic, failure: params.failure,
  } }),
  session: (id: number) => api.get(`${supportBase}/sessions/${id}`),
  messages: (id: number, params: SupportFilter) => api.get(`${supportBase}/sessions/${id}/messages`, { params }),
  content: (session: number, message: number) => api.get(`${supportBase}/sessions/${session}/messages/${message}/content`),
  tasks: (params: SupportFilter) => api.get(`${supportBase}/downstreams`, { params: taskFilter(params) }),
  upstreams: (params: SupportFilter) => api.get(`${supportBase}/upstreams`, { params: taskFilter(params) }),
  upstream: (id: number) => api.get(`${supportBase}/upstreams/${id}`),
  topics: (params: SupportFilter) => api.get(`${supportBase}/topic-operations`, { params: taskFilter(params) }),
  topic: (id: number) => api.get(`${supportBase}/topic-operations/${id}`),
  resolveUpstream: (id: number, input: SupportResolution) => api.post(`${supportBase}/upstreams/${id}/resolve`, {
    expected_version: input.expected_version, decision: input.decision, evidence: input.evidence, delivery_id: input.delivery_id,
  }),
  resolveTopic: (id: number, input: SupportTopicResolution) => api.post(`${supportBase}/topic-operations/${id}/resolve`, {
    expected_version: input.expected_version, decision: input.decision, evidence: input.evidence,
    thread_id: input.thread_id || '0', no_effect: input.no_effect,
  }),
  task: (id: number) => api.get(`${supportBase}/downstreams/${id}`),
  /** Explicit projection prevents accidental actor/generation/platform IDs from entering the request. */
  resolve: (id: number, input: SupportResolution) => api.post(`${supportBase}/downstreams/${id}/resolve`, {
    expected_version: input.expected_version, decision: input.decision, evidence: input.evidence, delivery_id: input.delivery_id,
  }),
}
