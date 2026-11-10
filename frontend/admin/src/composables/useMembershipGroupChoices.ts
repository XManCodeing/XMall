import { computed, getCurrentInstance, onActivated, onDeactivated, onScopeDispose, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramGroupsAPI, type TelegramGroup } from '@/api/telegramGroups'
import { groupReady, groupState } from './useTelegramGroups'
import type { MembershipDraft } from '@/utils/membershipProduct'

/** Manage product group choices without replacing the product draft. @param draft Live editor draft. @param open Editor lifetime. @returns Authorized choices, diagnostics and actions. */
export function useMembershipGroupChoices(draft: Ref<MembershipDraft>, open: Ref<boolean>) {
  const auth = useAdminAuthStore(), { t } = useI18n()
  const groups = ref<TelegramGroup[]>([]), groupsLoading = ref(false), groupError = ref(''), botFilter = ref('')
  const now = ref(Date.now())
  let generation = 0
  let timer: ReturnType<typeof setInterval> | undefined
  /** Resume freshness checks without reloading choices or replacing the draft. @returns Nothing. */
  function startClock() {
    if (timer !== undefined) return
    now.value = Date.now()
    timer = setInterval(() => { now.value = Date.now() }, 1000)
  }
  /** Suspend clock updates while the parent page is cached. @returns Nothing. */
  function stopClock() { if (timer !== undefined) clearInterval(timer); timer = undefined }
  startClock()
  if (getCurrentInstance()) { onActivated(startClock); onDeactivated(stopClock) }
  const canGroups = computed(() => auth.hasPermission('GET:/admin/telegram-bot/groups'))
  const selectedGroups = computed(() => groups.value.filter(group => draft.value.target_ids.includes(group.id)))
  const missingIDs = computed(() => draft.value.target_ids.filter(id => !groups.value.some(group => group.id === id)))
  const mixedBots = computed(() => new Set(selectedGroups.value.map(group => group.bot_instance_id)).size > 1)
  const visibleGroups = computed(() => groups.value.filter(group => draft.value.target_ids.includes(group.id) || !botFilter.value || String(group.bot_instance_id) === botFilter.value))
  const validGroups = computed(() => canGroups.value && !groupsLoading.value && !groupError.value && draft.value.target_ids.length > 0 &&
    !missingIDs.value.length && !mixedBots.value && selectedGroups.value.every(available))
  /** Match backend enabled/recovery gates and shared observation freshness. @param group API row. @returns Whether new binding is safe. */
  function available(group: TelegramGroup) { return group.purpose === 'membership' && group.enabled && !group.recovery_task_id && groupReady(group, now.value) }
  /** Display operational failures before generic freshness labels. @param group API row. @returns Localized state. */
  function status(group: TelegramGroup) {
    if (group.recovery_task_id) return t('telegramMembership.recovering')
    if (group.observation.error_class === 'anonymous_admin') return t('telegramBot.groups.values.anonymous_admin')
    if (!group.enabled) return t('telegramMembership.disabled')
    if (available(group)) return t('telegramMembership.available')
    return group.observation.state === 'ready' && group.missing_capabilities?.length
      ? t('telegramBot.groups.values.missing_permissions') : t(`telegramBot.groups.values.${groupState(group, now.value)}`)
  }
  /** Allow explicit removal of invalid selections but never introduce an unavailable group. @param id Target ID. @param checked Checkbox value. @returns Nothing. */
  function toggleTarget(id: number, checked: boolean | 'indeterminate') {
    if (checked !== true) { draft.value.target_ids = draft.value.target_ids.filter(target => target !== id); return }
    const group = groups.value.find(item => item.id === id)
    if (!canGroups.value || groupsLoading.value || groupError.value || !group || !available(group)) return
    draft.value.target_ids = [...new Set([...draft.value.target_ids, id])]
  }
  /** Load the complete authorized list; keep selected IDs and product fields through errors. @returns Completion promise. */
  async function loadGroups() {
    const request = ++generation
    if (!canGroups.value || !open.value) return
    groupsLoading.value = true; groupError.value = ''
    try {
      const found: TelegramGroup[] = []
      for (let page = 1; ; page++) {
        const response = await telegramGroupsAPI.list({ page, page_size: 100, purpose: 'membership' })
        if (request !== generation || !canGroups.value || !open.value) return
        const result = response.data.data
        const rows = result.items || []
        found.push(...rows.filter(group => group.purpose === 'membership'))
        if (page * (result.page_size || 100) >= result.total || rows.length === 0) break
      }
      groups.value = found
    } catch { if (request === generation) groupError.value = t('common.api.requestFailed') }
    finally { if (request === generation) groupsLoading.value = false }
  }
  /** Reset only choice state when the enclosing editor closes or identity changes. @returns Nothing. */
  function resetGroups() { generation++; groups.value = []; groupsLoading.value = false; groupError.value = ''; botFilter.value = '' }
  watch(open, value => { if (!value) resetGroups() }, { flush: 'sync' })
  watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], resetGroups, { flush: 'sync' })
  onScopeDispose(() => { generation++; stopClock() })
  /** Recheck wall-clock freshness at submit time as well as on the display timer. @returns Current binding validity. */
  function validateGroupsNow() { now.value = Date.now(); return validGroups.value }
  return { validateGroupsNow, canGroups, groupsLoading, groupError, botFilter, visibleGroups, missingIDs, mixedBots, validGroups, available, status, toggleTarget, loadGroups, resetGroups }
}
