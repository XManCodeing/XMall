import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { telegramGroupsAPI } from '@/api/telegramGroups'
import { validRecoveryBotID, validRecoveryChatID, type BotRouteDestination } from '@/api/telegramRecovery'
export interface BotRouteOption { id: string; name: string; chat: string }
/** Preserve decimal identifiers; reject already-rounded group list IDs. @param value Wire ID. @returns Canonical ID or empty. */
function wireID(value: unknown): string {
  const text = typeof value === 'string' ? value : typeof value === 'number' && Number.isSafeInteger(value) ? String(value) : ''
  return text === text.trim() && validRecoveryBotID(text) ? text : ''
}
/** Validate optional mappings against source-owned membership options. @param routes Draft. @param known Authorized options. @returns Valid bounded unique intent. */
export function validBotRoutes(routes: BotRouteDestination[], known: BotRouteOption[]): boolean {
  if (routes.length > 100) return false
  const targets = new Set<string>(), chats = new Set<string>()
  for (const route of routes) {
    const source = known.find(item => item.id === route.target_id)
    if (!source || !wireID(route.target_id) || typeof route.chat_id !== 'string' || route.chat_id !== route.chat_id.trim()
      || !validRecoveryChatID(route.chat_id) || targets.has(route.target_id) || chats.has(route.chat_id)) return false
    targets.add(route.target_id); chats.add(route.chat_id)
  }
  return true
}
/** Read one bounded membership page; source changes invalidate options, drafts and in-flight reads.
 * @param source Source Bot string ID. @param allowed Group list permission. @returns Mapping controls and safe serializer.
 */
export function useRecoveryBotRoutes(source: Ref<string>, allowed: Ref<boolean>) {
  const routes = ref<BotRouteDestination[]>([]), options = ref<BotRouteOption[]>([]), selectedOptions = ref<BotRouteOption[]>([])
  const loading = ref(false), error = ref(''), page = ref(1), total = ref(0)
  let request = 0
  const known = computed(() => [...selectedOptions.value, ...options.value])
  const valid = computed(() => !routes.value.length || allowed.value && !loading.value && !error.value && validBotRoutes(routes.value, known.value))
  /** Clear source-bound data and cancel late callbacks. @returns Nothing. */
  function clear() { request++; routes.value = []; options.value = []; selectedOptions.value = []; loading.value = false; error.value = ''; page.value = 1; total.value = 0 }
  /** Fetch actual current membership targets; never accept historical routes or another bot. @param requestedPage One-based page. @returns Completion promise. */
  async function load(requestedPage = 1) {
    if (!allowed.value || !wireID(source.value) || !Number.isSafeInteger(requestedPage) || requestedPage < 1) return
    const current = ++request, bot = source.value
    loading.value = true; error.value = ''; options.value = []
    try {
      const response = await telegramGroupsAPI.list({ bot_instance_id: bot, purpose: 'membership', page: requestedPage, page_size: 20 })
      if (current !== request || !allowed.value || source.value !== bot) return
      const payload = response.data.data
      if (!payload || !Array.isArray(payload.items) || payload.items.length > 20 || !Number.isSafeInteger(payload.total) || payload.total < 0) { error.value = 'invalid'; return }
      options.value = payload.items.filter((item) => item.purpose === 'membership' && wireID(item.bot_instance_id) === bot
        && wireID(item.id) && typeof item.name === 'string' && typeof item.telegram_chat_id === 'string' && validRecoveryChatID(item.telegram_chat_id))
        .map((item) => ({ id: wireID(item.id), name: item.name as string, chat: item.telegram_chat_id as string }))
      const visibleTargets = new Set(options.value.map(option => option.id))
      selectedOptions.value = selectedOptions.value.map(option => options.value.find(current => current.id === option.id) || option)
      // A selected ID absent from another page is not revoked; the backend rechecks the complete source snapshot.
      if (visibleTargets.size !== options.value.length) { options.value = []; error.value = 'invalid'; return }
      page.value = requestedPage; total.value = payload.total
    } catch { if (current === request) error.value = 'loadFailed' }
    finally { if (current === request) loading.value = false }
  }
  /** Add only a current-page source-owned target, at most 100. @param id Selected option. @returns Nothing. */
  function add(id: string) {
    const option = options.value.find(item => item.id === id)
    if (!allowed.value || loading.value || !option || routes.value.length >= 100 || routes.value.some(route => route.target_id === id)) return
    selectedOptions.value.push({ ...option }); routes.value.push({ target_id: id, chat_id: '' })
  }
  /** Remove one draft and its retained identity. @param id Target ID. @returns Nothing. */
  function remove(id: string) { routes.value = routes.value.filter(route => route.target_id !== id); selectedOptions.value = selectedOptions.value.filter(item => item.id !== id) }
  /** Emit intent fields only, omitting empty routes for backward compatibility. @returns Optional route payload. */
  function payload(): { routes?: BotRouteDestination[] } {
    return routes.value.length ? { routes: routes.value.map(route => ({ target_id: route.target_id, chat_id: route.chat_id })) } : {}
  }
  watch([source, allowed], clear, { flush: 'sync' }); onBeforeUnmount(clear)
  return { routes, options, selectedOptions, loading, error, page, total, valid, load, add, remove, clear, payload }
}
