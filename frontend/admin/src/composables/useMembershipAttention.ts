import { onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, watch } from 'vue'
import { notifyErrorOnce } from '@/utils/notify'
import i18n from '@/i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { membershipBase, telegramMembershipAPI } from '@/api/telegramMembership'

export interface MembershipAttentionState {
  count: number | null
  loading: boolean
  error: boolean
}
type AttentionSection = 'tasks' | 'reviews' | 'lifecycle'
const sections: AttentionSection[] = ['tasks', 'reviews', 'lifecycle']
const paths = { tasks: 'access/tasks', reviews: 'access/reviews', lifecycle: 'lifecycle/tasks' }
const readers = {
  tasks: telegramMembershipAPI.accessTasks,
  reviews: telegramMembershipAPI.accessReviews,
  lifecycle: telegramMembershipAPI.lifecycleTasks,
}

/** Read cross-bot exception totals independently of the visible list page.
 * @param enabledSections 当前页面展示的板块，默认包含全部会员任务。
 * @returns Reactive per-section status and a deduplicated explicit refresh action.
 */
export function useMembershipAttention(enabledSections: readonly AttentionSection[] = sections) {
  const auth = useAdminAuthStore()
  const states = reactive<Record<AttentionSection, MembershipAttentionState>>({
    tasks: { count: null, loading: false, error: false },
    reviews: { count: null, loading: false, error: false },
    lifecycle: { count: null, loading: false, error: false },
  })
  let generation = 0
  let disposed = false
  let mounted = false
  let pending: Promise<void> | undefined
  let timer: ReturnType<typeof setInterval> | undefined

  /** Check the original list permission; summary does not grant extra access.
   * @param section Section to query. @returns Whether its existing reader is authorized.
   */
  function allowed(section: AttentionSection): boolean {
    return enabledSections.includes(section) && Boolean(auth.token) && auth.hasPermission(`GET:${membershipBase}/${paths[section]}`)
  }

  /** Fetch one aggregate and reject malformed/old-server responses instead of claiming zero.
   * @param section Authorized section. @param request Authorization generation. @returns Completion.
   */
  async function read(section: AttentionSection, request: number): Promise<void> {
    if (!allowed(section)) return
    states[section].loading = true
    try {
      const response = await readers[section]({ summary: 1 })
      const count: unknown = response.data.data?.attention_count
      if (typeof count !== 'number' || !Number.isSafeInteger(count) || count < 0) {
        throw new Error('Invalid membership attention summary')
      }
      if (disposed || request !== generation || !allowed(section)) return
      states[section].count = count
      states[section].error = false
    } catch (cause) {
      // Keep unknown totals distinct from zero without repeating the API toast in each tab.
      if (disposed || request !== generation || !allowed(section)) return
      states[section].count = null
      states[section].error = true
      notifyErrorOnce(cause, i18n.global.t('common.api.requestFailed'))
    } finally {
      if (!disposed && request === generation) states[section].loading = false
    }
  }

  /** Refresh all authorized totals once; parallel clicks share the current request.
   * @returns Completion after each independently handled reader settles.
   */
  function refresh(): Promise<void> {
    if (disposed) return Promise.resolve()
    if (pending) return pending
    const request = generation
    const work = Promise.all(sections.map(section => read(section, request))).then(() => undefined)
    pending = work
    void work.finally(() => { if (pending === work) pending = undefined })
    return work
  }

  /** Poll only a visible browser page; no hidden-tab request loops. @returns Nothing. */
  function refreshVisible(): void {
    if (mounted && !document.hidden) void refresh()
  }

  /** Poll visible pages while readers are healthy; failures await an explicit refresh or visibility change.
   * @returns Nothing. Avoid repeatedly triggering global API error toasts during an outage.
   */
  function poll(): void {
    if (sections.some(section => allowed(section) && states[section].error)) return
    refreshVisible()
  }

  watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], () => {
    generation++
    pending = undefined
    for (const section of sections) Object.assign(states[section], { count: null, loading: false, error: false })
    if (mounted) void refresh()
  }, { flush: 'sync' })

  /** Resume visible summaries once, including when a cached page returns. @returns Nothing. */
  function startPolling(): void {
    if (disposed || timer !== undefined) return
    mounted = true
    refreshVisible()
    timer = setInterval(poll, 30000)
    document.addEventListener('visibilitychange', refreshVisible)
  }
  /** Suspend both timer and subscription while preserving existing summary state. @returns Nothing. */
  function stopPolling(): void {
    mounted = false
    if (timer !== undefined) clearInterval(timer)
    timer = undefined
    document.removeEventListener('visibilitychange', refreshVisible)
  }
  onMounted(startPolling)
  onActivated(startPolling)
  onDeactivated(stopPolling)
  onBeforeUnmount(() => { disposed = true; generation++; stopPolling() })
  return { states, refresh }
}
