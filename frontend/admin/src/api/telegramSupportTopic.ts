import { api } from './client'
import { supportBase, validSupportEvidence, validSupportThread } from './telegramSupport'

export const topicDeletionPermission = `POST:${supportBase}/topic-operations/:id/confirm-deleted`
export interface TopicMappingSnapshot { id: string; version: string; chat_id: string; thread_id: string; route_version: string; state: string }
export interface TopicDeletionInput { expected_version: string; mapping_id: string; mapping_version: string; chat_id: string; thread_id: string; evidence: string; confirmed: boolean }
/** Validate lossless decimal identifiers and explicit verified-deletion consent. */
export function validTopicDeletion(input: TopicDeletionInput): boolean {
  return validSupportThread(input.expected_version) && validSupportThread(input.mapping_id) && validSupportThread(input.mapping_version)
    && /^-[1-9]\d{0,18}$/.test(input.chat_id) && BigInt(input.chat_id) >= BigInt('-9223372036854775808')
    && validSupportThread(input.thread_id) && validSupportEvidence(input.evidence) && input.confirmed === true
}
/** Project only confirmation fields; no bot/actor/generation can be supplied. */
export function confirmTopicDeletion(id: number, input: TopicDeletionInput) {
  return api.post(`${supportBase}/topic-operations/${id}/confirm-deleted`, {
    expected_version: input.expected_version, mapping_id: input.mapping_id, mapping_version: input.mapping_version,
    chat_id: input.chat_id, thread_id: input.thread_id, evidence: input.evidence, confirmed: input.confirmed,
  })
}
