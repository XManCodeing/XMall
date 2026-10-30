import { computed, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminTelegramBroadcastDelivery } from '@/api/types'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyErrorOnce, notifySuccess } from '@/utils/notify'

export function useTelegramBroadcastDeliveries(
  broadcastId: Ref<number>,
  enabled: Ref<boolean>,
  onRetried?: () => void | Promise<void>,
) {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const items = ref<AdminTelegramBroadcastDelivery[]>([])
  const loading = ref(false)
  const retrying = ref<number | null>(null)
  const error = ref('')
  const pagination = ref({ page: 1, page_size: 20, total: 0, total_page: 1 })
  let generation = 0
  let requestID = 0
  const canList = computed(() => auth.hasPermission(`GET:/admin/telegram-bot/broadcasts/${broadcastId.value}/deliveries`))
  const canRetry = (item: AdminTelegramBroadcastDelivery) => enabled.value && canList.value
    && item.broadcast_id === broadcastId.value && item.status === 'failed' && item.retryable === true
    && auth.hasPermission(`POST:/admin/telegram-bot/broadcasts/${broadcastId.value}/deliveries/${item.id}/retry`)

  const fetchPage = async (page = pagination.value.page, options: { silent?: boolean } = {}) => {
    if (!enabled.value || !canList.value) return
    const silent = options.silent === true && items.value.length > 0
    if (!silent && loading.value) return
    const request = ++requestID
    if (!silent) {
      loading.value = true
      error.value = ''
    }
    try {
      const res = await adminAPI.getTelegramBroadcastDeliveries(broadcastId.value, { page, page_size: 20 })
      if (request !== requestID) return
      items.value = res.data?.data || []
      pagination.value = res.data?.pagination || { page, page_size: 20, total: 0, total_page: 1 }
      if (!silent) error.value = ''
    } catch {
      if (request === requestID && !silent) {
        items.value = []
        error.value = t('telegramBot.instances.recipientLoadFailed')
      }
    } finally {
      if (request === requestID) loading.value = false
    }
  }

  const retry = async (item: AdminTelegramBroadcastDelivery) => {
    if (loading.value || retrying.value !== null || !items.value.includes(item) || !canRetry(item)) return false
    const currentGeneration = generation
    const id = broadcastId.value
    retrying.value = item.id
    // An ambiguous response must not allow another submission before reloading.
    item.retryable = false
    try {
      const res = await adminAPI.retryTelegramBroadcastDelivery(id, item.id)
      if (currentGeneration !== generation) return false
      if (res.data?.data?.accepted !== true) throw new Error('Retry was not accepted')
      notifySuccess(t('telegramBot.instances.retryAccepted'))
      await fetchPage()
      if (onRetried) await onRetried()
      return true
    } catch (err) {
      if (currentGeneration === generation) notifyErrorOnce(err, t('telegramBot.instances.retryFailed'))
      return false
    } finally {
      if (currentGeneration === generation) retrying.value = null
    }
  }

  watch([broadcastId, enabled, canList], () => {
    ++generation; ++requestID
    items.value = []; error.value = ''
    loading.value = false; retrying.value = null
    pagination.value = { page: 1, page_size: 20, total: 0, total_page: 1 }
    void fetchPage(1)
  }, { immediate: true, flush: 'sync' })

  return { items, loading, retrying, error, pagination, canList, canRetry, fetchPage, retry }
}
