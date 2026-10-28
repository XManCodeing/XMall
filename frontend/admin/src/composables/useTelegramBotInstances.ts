import { computed, getCurrentScope, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAdminAuthStore } from '@/stores/auth'

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

export function useTelegramBotInstances() {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const instances = ref<BotInstance[]>([])
  const loading = ref(false)
  const busy = ref(false)
  const error = ref('')
  const refreshError = ref('')
  const notice = ref('')
  const now = ref(Date.now())
  const canList = computed(() => auth.hasPermission('GET:/admin/telegram-bot/instances'))
  const canCreate = computed(() => auth.hasPermission('POST:/admin/telegram-bot/instances'))
  const can = (method: string, id: number, suffix = '') => auth.hasPermission(`${method}:/admin/telegram-bot/instances/${id}${suffix}`)
  const fail = (err: any) => { error.value = t(err?.response?.status === 409 ? 'telegramBot.instances.conflict' : 'telegramBot.instances.failed') }
  let disposed = false
  let pending: Promise<void> | undefined
  let timer: ReturnType<typeof setInterval> | undefined
  const refresh = (): Promise<void> => {
    if (disposed || !canList.value) return Promise.resolve()
    if (pending) return pending
    loading.value = true
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
        if (!disposed) instances.value = items
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
  const startPolling = () => {
    if (disposed || timer !== undefined) return
    void refresh()
    timer = setInterval(() => {
      now.value = Date.now()
      if (!busy.value) void refresh()
    }, 10000)
  }
  if (getCurrentScope()) onScopeDispose(() => {
    disposed = true
    if (timer !== undefined) clearInterval(timer)
  })
  const run = async (action: () => Promise<unknown>) => {
    if (disposed || busy.value) return false
    busy.value = true
    error.value = ''; notice.value = ''
    try {
      // Drain an existing poll before mutating; refresh after the mutation must
      // not accidentally reuse a pre-mutation request.
      await pending
      if (disposed) return false
      await action()
      if (disposed) return false
      notice.value = t('telegramBot.instances.success')
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
  const test = (item: BotInstance) => {
    if (!can('POST', item.id, '/test')) return Promise.resolve(false)
    return run(() => adminAPI.testTelegramBotInstance(item.id))
  }
  return { instances, loading, busy, error, refreshError, notice, now, canList, canCreate, can, refresh, startPolling, create, patch, test }
}
