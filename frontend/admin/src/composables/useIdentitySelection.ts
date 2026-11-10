import { computed, onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { membershipBase, telegramMembershipAPI, type MembershipOrder } from '@/api/telegramMembership'
import { validIdentityID } from '@/api/telegramIdentity'
import { useAdminAuthStore } from '@/stores/auth'

interface Choice { id: string; label: string }
/** Accept only lossless API identifiers; numeric endpoints cannot address unsafe integers.
 * @param value API identifier. @returns Canonical decimal ID or empty string.
 */
function safeID(value: unknown): string {
  if (typeof value === 'number') return Number.isSafeInteger(value) && value > 0 ? String(value) : ''
  return validIdentityID(value) && BigInt(value) <= BigInt(Number.MAX_SAFE_INTEGER) ? value : ''
}
/** Read existing user and membership-order APIs without inventing an entitlement endpoint.
 * @param account Selected website account. @param oldID Reviewed old Telegram identity.
 * @param locked Whether the migration draft is immutable. @returns Bounded read-only selector state.
 */
export function useIdentitySelection(account: Ref<string>, oldID: Ref<string>, locked: Ref<boolean>) {
  const auth = useAdminAuthStore(), { t } = useI18n()
  const keyword = ref(''), users = ref<Choice[]>([]), orders = ref<Choice[]>([]), targets = ref<string[]>([])
  const userPage = ref(1), orderPage = ref(1), userNext = ref(false), orderNext = ref(false)
  const busy = ref(false), error = ref(''), orderID = ref('')
  let generation = 0
  const canUsers = computed(() => auth.hasPermission('GET:/admin/users'))
  const canOrders = computed(() => auth.hasPermission(`GET:${membershipBase}/orders`))
  /** Check the precise order detail permission. @param id Order ID. @returns Allowed read. */
  const canOrder = (id: string) => auth.hasPermission(`GET:${membershipBase}/orders/${id}`)
  /** Invalidate reads and remove cross-account candidates. @returns Nothing. */
  function clear() {
    generation++; busy.value = false; error.value = ''; orders.value = []; targets.value = []; orderID.value = ''
    orderPage.value = 1; orderNext.value = false
  }
  /** Fetch one bounded candidate page with explicit permission and lossless IDs.
   * @param kind Directory to query. @param page One-based page. @returns Completion promise.
   */
  async function load(kind: 'users' | 'orders', page = 1) {
    if (locked.value || busy.value || !Number.isSafeInteger(page) || page < 1) return
    if (kind === 'users' ? !canUsers.value : !canOrders.value || !validIdentityID(account.value)) return
    const request = ++generation
    busy.value = true; error.value = ''
    if (kind === 'users') users.value = []
    else { orders.value = []; targets.value = []; orderID.value = '' }
    try {
      const response = kind === 'users'
        ? await adminAPI.getUsers({ keyword: keyword.value.trim() || undefined, page, page_size: 20 })
        : await telegramMembershipAPI.orders({ user_id: account.value, page, page_size: 20 })
      if (request !== generation || locked.value || !(kind === 'users' ? canUsers.value : canOrders.value)) return
      const rows = response.data.data
      if (!Array.isArray(rows)) throw new Error('Invalid candidate list')
      const choices = rows.flatMap(row => {
        const id = safeID(row?.id)
        if (!id || (kind === 'orders' && safeID(row.user_id) !== account.value)) return []
        return [{ id, label: kind === 'users' ? String(row.nickname || row.email || id) : String(row.order_no || id) }]
      })
      const next = typeof response.data.pagination?.total === 'number'
        ? page * 20 < response.data.pagination.total : rows.length === 20
      if (kind === 'users') { users.value = choices; userPage.value = page; userNext.value = next }
      else { orders.value = choices; orderPage.value = page; orderNext.value = next }
    } catch { if (request === generation) error.value = t('telegramRecovery.identity.lookupFailed') }
    finally { if (request === generation) busy.value = false }
  }
  /** Clear only the directory keyword, retaining the reviewed migration account and selection.
   * @returns Completion promise; locked, busy or unauthorized drafts remain unchanged.
   */
  async function resetUsers() {
    if (locked.value || busy.value || !canUsers.value) return
    keyword.value = ''; userPage.value = 1; userNext.value = false
    await load('users', 1)
  }
  /** Read only an authorized order and derive old-identity effective ledger candidates.
   * @param id Selected order ID. @returns Completion promise; never requests an invitation or mutation.
   */
  async function selectOrder(id: string) {
    if (locked.value || busy.value || !safeID(id) || !canOrder(id) || !orders.value.some(order => order.id === id) || !validIdentityID(oldID.value)) return
    const request = ++generation
    busy.value = true; error.value = ''; targets.value = []; orderID.value = id
    try {
      const response = await telegramMembershipAPI.order(Number(id))
      if (request !== generation || locked.value || !canOrder(id)) return
      const order = response.data.data as MembershipOrder
      if (safeID(order?.id) !== id || safeID(order?.user_id) !== account.value || !Array.isArray(order.items)) throw new Error('Invalid account scope')
      const candidates = order.items.flatMap(item => {
        if (safeID(item.membership_snapshot?.account_id) !== account.value || item.membership_ledger?.state !== 'recorded') return []
        return (item.membership_ledger.effective || []).flatMap(target => {
          const targetID = safeID(target.target_id)
          return targetID && target.beneficiary_telegram_id === oldID.value ? [targetID] : []
        })
      })
      targets.value = [...new Set(candidates)]
    } catch { if (request === generation) error.value = t('telegramRecovery.identity.lookupFailed') }
    finally { if (request === generation) busy.value = false }
  }
  watch([account, oldID, locked], clear, { flush: 'sync' })
  watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], () => {
    clear(); users.value = []; keyword.value = ''; userPage.value = 1; userNext.value = false
  }, { flush: 'sync' })
  onBeforeUnmount(clear)
  return { keyword, users, orders, targets, userPage, orderPage, userNext, orderNext, busy, error, orderID, canUsers, canOrders, canOrder, load, resetUsers, selectOrder }
}
