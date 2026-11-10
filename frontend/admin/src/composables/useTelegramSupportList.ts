import { computed, onUnmounted, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyErrorOnce } from '@/utils/notify'
import { supportBase, supportPositive, supportTaskStates, telegramSupportAPI, type SupportSession, type SupportTaskRecord, type SupportTaskResource } from '@/api/telegramSupport'

/** Create a bounded metadata list for a reactive resource; never request message content. */
export function useTelegramSupportList(resource: MaybeRefOrGetter<'sessions' | SupportTaskResource>) {
  const { t } = useI18n(), auth = useAdminAuthStore()
  const selected = computed(() => toValue(resource))
  const can = (method = 'GET', suffix = '') => auth.hasPermission(`${method}:${supportBase}/${selected.value}${suffix}`)
  const canList = computed(() => can())
  const items = ref<Array<SupportSession | SupportTaskRecord>>([])
  const page = ref(1), total = ref(0), loading = ref(false), error = ref('')
  const botFilter = ref(''), stateFilter = ref('all')
  const unrepliedFilter = ref('all'), blockedFilter = ref('all'), topicFilter = ref('all'), failureFilter = ref('all')
  const sessionFiltersValid = computed(() => [unrepliedFilter.value, blockedFilter.value, topicFilter.value].every(value => ['all', 'true', 'false'].includes(value)) && ['all', 'any', 'upstream', 'downstream', 'none'].includes(failureFilter.value))
  const requiresBot = computed(() => selected.value === 'upstreams' || selected.value === 'topic-operations')
  const validFilter = computed(() => {
    const botValid = botFilter.value ? /^[1-9]\d*$/.test(botFilter.value) && supportPositive(Number(botFilter.value)) : !requiresBot.value
    return botValid && (selected.value === 'sessions' ? sessionFiltersValid.value && ['all', 'waiting_verification', 'open', 'closed', 'cooldown'].includes(stateFilter.value) : stateFilter.value === 'all' || supportTaskStates[selected.value].includes(stateFilter.value))
  })
  const pageSize = ref(20)
  let request = 0
  /** Invalidate pending results immediately when query identity changes. */
  function clear() { request++; items.value = []; total.value = 0; error.value = ''; loading.value = false; page.value = 1 }
  /** Load the selected metadata page; stale responses or revoked permissions cannot restore rows. */
  async function load(nextPage = page.value) {
    const current = ++request, kind = selected.value
    items.value = []; error.value = ''; loading.value = false
    if (!canList.value || !validFilter.value || !supportPositive(nextPage) || nextPage > 1000000) return
    loading.value = true
    try {
      const params = { page: nextPage, page_size: pageSize.value, bot_id: botFilter.value ? Number(botFilter.value) : undefined, state: stateFilter.value === 'all' ? undefined : stateFilter.value }
      const fetch = { sessions: telegramSupportAPI.sessions, downstreams: telegramSupportAPI.tasks, upstreams: telegramSupportAPI.upstreams, 'topic-operations': telegramSupportAPI.topics }[kind]
      const response = kind === 'sessions' ? await telegramSupportAPI.sessions({ ...params,
        unreplied: unrepliedFilter.value === 'all' ? undefined : unrepliedFilter.value === 'true',
        blocked: blockedFilter.value === 'all' ? undefined : blockedFilter.value === 'true',
        has_topic: topicFilter.value === 'all' ? undefined : topicFilter.value === 'true',
        failure: failureFilter.value === 'all' ? undefined : failureFilter.value as 'any' | 'upstream' | 'downstream' | 'none',
      }) : await fetch(params)
      if (request !== current || !canList.value) return
      if (!Array.isArray(response.data?.data?.items)) throw new Error(t('common.api.responseMissing'))
      items.value = response.data.data.items; total.value = response.data.data.total; page.value = nextPage
    } catch (cause) {
      if (request === current) {
        error.value = t('telegramSupport.failed')
        notifyErrorOnce(cause, error.value)
      }
    }
    finally { if (request === current) loading.value = false }
  }
  /** Search current inputs from page one. No parameters; returns the single list request. */
  function search() { return load(1) }
  /** Restore filter defaults without changing resource scope; returns one guarded first-page request. */
  function resetFilters() {
    botFilter.value = ''; stateFilter.value = 'all'
    unrepliedFilter.value = 'all'; blockedFilter.value = 'all'; topicFilter.value = 'all'; failureFilter.value = 'all'
    page.value = 1
    return load(1)
  }
  watch(selected, () => { clear(); stateFilter.value = 'all' }, { flush: 'sync' })
  watch([botFilter, stateFilter, canList], clear, { flush: 'sync' })
  watch([unrepliedFilter, blockedFilter, topicFilter, failureFilter], () => { if (selected.value === 'sessions') clear() }, { flush: 'sync' })
  onUnmounted(() => { request++ })
  return { can, canList, items, page, total, pageSize, loading, error, botFilter, stateFilter, unrepliedFilter, blockedFilter, topicFilter, failureFilter, validFilter, requiresBot, load, search, resetFilters }
}
