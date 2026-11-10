import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { adminAPI } from '@/api/admin'
import { useMembershipGroupChoices } from './useMembershipGroupChoices'
import { membershipBase, telegramMembershipAPI, type MembershipProduct } from '@/api/telegramMembership'
import type { AdminCategory } from '@/api/types'
import { membershipDraft, membershipPayload, validMembershipDraft } from '@/utils/membershipProduct'
import { useProductSaveRecovery } from './useProductSaveRecovery'
import { confirmAction } from '@/utils/confirm'

/** Membership editor owns one versioned draft; permissions and account changes invalidate all in-flight reads. */
export function useMembershipProductEditor() {
  const auth = useAdminAuthStore()
  const { t } = useI18n()
  const open = ref(false), loading = ref(false), saving = ref(false), conflict = ref(false)
  const recovery = useProductSaveRecovery(true)
  const error = ref('')
  const product = ref<MembershipProduct>(), draft = ref(membershipDraft())
  const categories = ref<AdminCategory[]>([])
  const groupChoices = useMembershipGroupChoices(draft, open)
  const { loadGroups, resetGroups, validGroups } = groupChoices
  const requestedID = ref<number>()
  let generation = 0
  const can = (method: string, id?: number) => auth.hasPermission(`${method}:${membershipBase}/products${id ? `/${id}` : ''}`)
  const canCategories = computed(() => auth.hasPermission('GET:/admin/categories'))
  const canSave = computed(() => can(product.value ? 'PUT' : 'POST', product.value?.id))
  const valid = computed(() => validMembershipDraft(draft.value) && validGroups.value)
  /** Open a new or existing authorized editor. Failed detail reads never enable creation over an existing product. */
  async function edit(id?: number) {
    if (recovery.unknown.value || saving.value) { open.value = true; return }
    if (!can(id ? 'GET' : 'POST', id)) return
    const request = ++generation
    open.value = true; loading.value = true; error.value = ''; conflict.value = false
    requestedID.value = id
    product.value = undefined; draft.value = membershipDraft(); resetGroups(); categories.value = []
    void loadGroups()
    try {
      if (id) {
        const response = await telegramMembershipAPI.product(id)
        if (request !== generation) return
        product.value = response.data.data
        draft.value = membershipDraft(product.value)
      }
      if (canCategories.value) {
        const response = await adminAPI.getCategories({ type: 'product' })
        if (request !== generation) return
        categories.value = response.data.data || []
      }
    } catch { if (request === generation) { error.value = t('common.api.requestFailed'); conflict.value = true } }
    finally { if (request === generation) loading.value = false }
  }
  /** @returns Completion of an explicit close; unknown drafts are retained in this editor. */
  async function close() { if (!saving.value && await recovery.allowClose()) open.value = false }
  /** Explicitly discard a conflicted draft only after confirmation, then require another Save. */
  async function reload() {
    const id = requestedID.value
    if (!id || saving.value || !can('GET', id)) return
    if (await confirmAction({ description: t('telegramMembership.reloadConfirm') })) await edit(id)
  }
  /** Save SKU prices and membership configuration with one POST/PUT; 409 preserves all draft fields. */
  async function save(): Promise<boolean> {
    if (recovery.unknown.value || !canSave.value || !groupChoices.validateGroupsNow() || !valid.value || loading.value || saving.value || conflict.value) return false
    const request = generation, id = product.value?.id, submittedSlug = draft.value.slug
    saving.value = true; error.value = ''
    try {
      const payload = membershipPayload(draft.value, product.value)
      if (id) await telegramMembershipAPI.update(id, payload)
      else await telegramMembershipAPI.create(payload)
      if (request !== generation) return false
      open.value = false
      return true
    } catch (failure: unknown) {
      if (request !== generation) return false
      if (recovery.capture(failure, id, submittedSlug)) return false
      conflict.value = (failure as { response?: { status?: number } })?.response?.status === 409
      error.value = conflict.value ? t('telegramMembership.conflict') : failure instanceof Error ? failure.message : t('common.api.requestFailed')
      return false
    } finally { if (request === generation) saving.value = false }
  }
  /** Permission/account replacement clears confidential drafts and makes pending responses stale. */
  watch(() => [auth.token, auth.isSuper, JSON.stringify(auth.permissions)], () => {
    generation++; recovery.reset(); open.value = false; product.value = undefined; draft.value = membershipDraft()
    resetGroups(); categories.value = []; loading.value = false; saving.value = false
  }, { flush: 'sync' })
  watch(open, value => { if (!value && !saving.value) generation++ })
  onScopeDispose(() => { generation++ })
  return { recovery, close, ...groupChoices, requestedID, open, loading, saving, conflict, error, product, draft, categories,
    can, canCategories, canSave, valid, edit, reload, save }
}
