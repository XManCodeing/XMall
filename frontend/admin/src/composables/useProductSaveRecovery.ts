import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { adminAPI } from '@/api/admin'
import { telegramMembershipAPI, membershipBase } from '@/api/telegramMembership'
import { isResultUnknown } from '@/api/client'
import { confirmAction } from '@/utils/confirm'
import { adminUrl } from '@/utils/adminBase'
import { productSaveMessages } from '@/i18n/productSave'

interface SavedCandidate { id: number; slug: string }
/**
 * Keep an uncertain submission locked for the lifetime of its form; reconciliation never writes.
 * @param membership Select the existing membership or ordinary product endpoints.
 * @returns Per-editor guard, captured submission identity and read-only reconciliation actions.
 */
export function useProductSaveRecovery(membership: boolean) {
  const auth = useAdminAuthStore()
  const { locale } = useI18n()
  const messages = computed(() => productSaveMessages[locale.value as keyof typeof productSaveMessages] || productSaveMessages['en-US'])
  const unknown = ref(false), checking = ref(false), candidate = ref<SavedCandidate>(), notice = ref('')
  const identity = ref<{ id?: number; slug: string }>({ slug: '' })
  let generation = 0
  const base = membership ? `${membershipBase}/products` : '/admin/products'
  const canCheck = computed(() => auth.hasPermission(`GET:${base}${identity.value.id ? `/${identity.value.id}` : ''}`))
  /** @param error Caught save error. @param id Submitted product ID. @param slug Submitted exact slug. @returns Whether save is now locked. */
  function capture(error: unknown, id: number | undefined, slug: string): boolean {
    if (!isResultUnknown(error)) return false
    identity.value = { id, slug: slug.trim() }; unknown.value = true; candidate.value = undefined; notice.value = ''
    return true
  }
  /** @returns Nothing. Reset only on account change, never on dialog close/open. */
  function reset(): void { generation++; unknown.value = false; checking.value = false; candidate.value = undefined; notice.value = '' }
  /** @returns Whether closing is explicitly approved; the caller retains the existing form in memory. */
  async function allowClose(): Promise<boolean> {
    return !unknown.value || await confirmAction({ description: messages.value.close })
  }
  /** @returns Completion of GET-only verification against the captured ID or exact slug. */
  async function check(): Promise<void> {
    if (!unknown.value || checking.value) return
    if (!canCheck.value) { notice.value = messages.value.permission; return }
    const { id, slug } = identity.value
    if (!id && !slug) { notice.value = messages.value.emptySlug; return }
    const request = ++generation
    checking.value = true; candidate.value = undefined; notice.value = ''
    try {
      const response = id
        ? await (membership ? telegramMembershipAPI.product(id) : adminAPI.getProduct(id))
        : await (membership ? telegramMembershipAPI.products({ slug, search: slug, page: 1, page_size: 100 }) : adminAPI.getProducts({ slug, search: slug, page: 1, page_size: 100 }))
      if (request !== generation || !canCheck.value) return
      const rows = id ? [response.data?.data] : response.data?.data
      if (!Array.isArray(rows)) throw new Error(messages.value.invalidResponse)
      const found = rows.find((row: SavedCandidate) => Number.isSafeInteger(row?.id) && row.id > 0 && (id ? row.id === id : row.slug === slug))
      candidate.value = found ? { id: found.id, slug: found.slug } : undefined
      notice.value = found ? messages.value.found : messages.value.notFound
    } catch (error: unknown) {
      if (request === generation) notice.value = error instanceof Error ? error.message : messages.value.invalidResponse
    } finally { if (request === generation) checking.value = false }
  }
  /** @returns Completion of confirmed new-tab navigation; this editor and its locked draft remain untouched. */
  async function openCandidate(): Promise<void> {
    const found = candidate.value, request = generation
    if (!found || !auth.hasPermission(`GET:${base}/${found.id}`)) return
    if (!await confirmAction({ description: messages.value.confirm }) || request !== generation || !auth.hasPermission(`GET:${base}/${found.id}`)) return
    const route = membership ? '/telegram-bot/membership/products' : '/products'
    window.open(adminUrl(`${route}?product_id=${found.id}`), '_blank', 'noopener,noreferrer')
  }
  return { unknown, checking, candidate, notice, messages, canCheck, capture, reset, allowClose, check, openCandidate }
}
export type ProductSaveRecovery = ReturnType<typeof useProductSaveRecovery>
