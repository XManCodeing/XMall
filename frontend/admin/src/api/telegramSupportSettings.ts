import { api } from './client'

/** Editable per-Bot consultation limits; durations use whole seconds. */
export interface SupportPolicy {
  max_messages: number
  max_bytes: number
  max_attempts: number
  retention_seconds: number
  challenge_ttl_seconds: number
  cooldown_seconds: number
}

/** Server-owned policy revision and deployment gate; not an enable request. */
export interface SupportSettings {
  bot_id: number
  version: number
  using_defaults: boolean
  policy: SupportPolicy
  defaults: SupportPolicy
  verification_mode: string
  production_ready: boolean
}

const base = '/admin/telegram-bot/support/settings'

/** Read the selected Bot's policy without exposing runtime credentials. */
export function getSupportSettings(botID: number) {
  return api.get(`${base}/${botID}`)
}

/** Replace all bounded policy fields using the loaded server revision. */
export function saveSupportSettings(botID: number, version: number, policy: SupportPolicy) {
  return api.put(`${base}/${botID}`, {
    expected_version: version,
    policy: {
      max_messages: policy.max_messages,
      max_bytes: policy.max_bytes,
      max_attempts: policy.max_attempts,
      retention_seconds: policy.retention_seconds,
      challenge_ttl_seconds: policy.challenge_ttl_seconds,
      cooldown_seconds: policy.cooldown_seconds,
    },
  })
}
