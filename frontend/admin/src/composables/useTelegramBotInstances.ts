import { computed, getCurrentScope, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyErrorOnce, notifyInfo, notifySuccess } from '@/utils/notify'

export interface BotInstance {
  id: number
  telegram_id: number
  username: string
  channel_client_id?: number
  token_set: boolean
  enabled: boolean
  mode: string
  version: number
  applied_version: number
  state: string
  last_error?: string
  last_seen_at?: string
  backlog?: number
  unknown?: number
}

export function botState(instance: BotInstance, now = Date.now()) {
  if (instance.mode !== 'internal') return 'external'
  if (instance.state === 'stopped') return 'stopped'
  const seen = Date.parse(instance.last_seen_at ?? '')
  // Match the runtime's 45-second heartbeat window; enabled is only intent.
  if (!Number.isFinite(seen) || now - seen >= 45000) return 'stale'
  return instance.state || 'unknown'
}

export function botStateVariant(instance: BotInstance, now = Date.now()) {
  const state = botState(instance, now)
  if (state === 'running') return 'success'
  if (state === 'stopped' || state === 'error' || state === 'stale') return 'destructive'
  return 'secondary'
}

export function useTelegramBotInstances() {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const instances = ref<BotInstance[]>([])
  const loading = ref(false)
  const busy = ref(false)
  const testingId = ref<number | null>(null)
  const refreshError = ref('')
  const now = ref(Date.now())
  const canList = computed(() => auth.hasPermission('GET:/admin/telegram-bot/instances'))
  const canCreate = computed(() => auth.hasPermission('POST:/admin/telegram-bot/instances'))
  const can = (method: string, id: number, suffix = '') => auth.hasPermission(`${method}:/admin/telegram-bot/instances/${id}${suffix}`)
  const fail = (err: unknown) => {
    const status = (err as { response?: { status?: number } } | null)?.response?.status
    notifyErrorOnce(err, t(status === 409 ? 'telegramBot.instances.conflict' : 'telegramBot.instances.failed'))
  }
  let disposed = false
  let hasLoaded = false
  let pending: Promise<void> | undefined
  let timer: ReturnType<typeof setInterval> | undefined
  const refresh = (): Promise<void> => {
    if (disposed || !canList.value) return Promise.resolve()
    if (pending) return pending
    // Only the first snapshot needs a loading state. Subsequent requests keep
    // the table and its actions stable, including when the snapshot is empty.
    loading.value = !hasLoaded
    pending = (async () => {
      let failed = false
      try {
        const res = await adminAPI.getTelegramBotInstances()
        if (disposed) return
        // Publish complete snapshots, never mutate an object held by an editor.
        const items: BotInstance[] = res.data.data.items.map((item: BotInstance) => ({ ...item }))
        await Promise.all(items.map(async item => {
          if (can('GET', item.id, '/status')) {
            try {
              const status = await adminAPI.getTelegramBotInstanceStatus(item.id)
              Object.assign(item, status.data.data)
            } catch { failed = true }
          }
        }))
        if (!disposed) {
          instances.value = items
          hasLoaded = true
        }
      } catch { failed = true } finally {
        if (!disposed) {
          refreshError.value = failed ? t('telegramBot.instances.failed') : ''
          now.value = Date.now()
          loading.value = false
        }
        pending = undefined
      }
    })()
    return pending
  }
  const poll = () => {
    if (disposed || (typeof document !== 'undefined' && document.hidden)) return
    now.value = Date.now()
    if (!busy.value) void refresh()
  }
  const startPolling = () => {
    if (disposed || timer !== undefined) return
    poll()
    timer = setInterval(poll, 10000)
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', poll)
  }
  if (getCurrentScope()) onScopeDispose(() => {
    disposed = true
    if (timer !== undefined) clearInterval(timer)
    if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', poll)
  })
  const run = async (action: () => Promise<unknown>) => {
    if (disposed || busy.value) return false
    busy.value = true
    try {
      // Drain an existing poll before mutating; refresh after the mutation must
      // not accidentally reuse a pre-mutation request.
      await pending
      if (disposed) return false
      await action()
      if (disposed) return false
      notifySuccess(t('telegramBot.instances.success'))
      await refresh()
      return !disposed
    } catch (err) { if (!disposed) fail(err); return false } finally { if (!disposed) busy.value = false }
  }
  const create = (token: string) => {
    if (!canCreate.value || !token.trim()) return Promise.resolve(false)
    return run(() => adminAPI.createTelegramBotInstance({ token: token.trim() }))
  }
  const patch = (item: BotInstance, data: { token?: string; enabled?: boolean }) => {
    if (!can('PATCH', item.id)) return Promise.resolve(false)
    return run(() => adminAPI.patchTelegramBotInstance(item.id, { expected_version: item.version, ...data }))
  }
  const test = async (item: BotInstance) => {
    if (disposed || busy.value || !can('POST', item.id, '/test')) return false
    busy.value = true
    testingId.value = item.id
    const bot = item.username ? `@${item.username}` : String(item.telegram_id)
    try {
      const res = await adminAPI.testTelegramBotInstance(item.id)
      if (disposed) return false
      const result = res.data?.data
      if (result?.ok !== true || typeof result.webhook_present !== 'boolean' || result.telegram_id !== item.telegram_id) {
        throw new Error('Invalid connection test result')
      }
      const name = result.username ? `@${result.username}` : bot
      const message = t('telegramBot.instances.testSuccess', { bot: name })
      if (result.webhook_present) {
        notifyInfo(`${message} ${t('telegramBot.instances.testWebhook')}`)
      } else {
        notifySuccess(message)
      }
      return true
    } catch (err) {
      if (!disposed) {
        // The API does not expose a reliable failure category. Do not guess a
        // token/network cause or repeat raw upstream errors containing secrets.
        notifyErrorOnce(err, t('telegramBot.instances.testFailed', { bot }))
      }
      return false
    } finally {
      if (!disposed) { busy.value = false; testingId.value = null }
    }
  }
  return { instances, loading, busy, testingId, refreshError, now, canList, canCreate, can, refresh, startPolling, create, patch, test }
}
