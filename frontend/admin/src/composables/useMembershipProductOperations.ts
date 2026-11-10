import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { telegramMembershipAPI, type MembershipProduct } from '@/api/telegramMembership'
import { isResultUnknown } from '@/api/client'
import { confirmAction } from '@/utils/confirm'
import { notifyErrorOnce } from '@/utils/notify'
import { getLocalizedText } from '@/utils/format'

type Operation = 'activate' | 'deactivate' | 'remove'
interface OperationError {
  message?: string
  resultUnknown?: boolean
  outcome?: string
  response?: { status?: number; data?: { msg?: string; message?: string } }
}

/**
 * Guard membership row writes by permission, version and session identity.
 * @param can Permission checker for the dedicated membership resource.
 * @param refresh Read-only list refresh; true only when a valid list was read.
 * @returns Reactive operation feedback and handlers; never optimistically updates rows.
 */
export function useMembershipProductOperations(
  can: (method: string, id?: number) => boolean,
  refresh: () => Promise<boolean>,
) {
  const { t } = useI18n()
  const pendingId = ref<number | null>(null)
  const notice = ref('')
  const blockedVersions = ref(new Map<number, number>())
  let session = 0

  /** @param product Current server row. @returns Whether its version is a safe positive integer. */
  function validVersion(product: MembershipProduct): boolean {
    return Number.isSafeInteger(product.membership.version) && product.membership.version > 0
  }

  /** @param product Current server row. @returns Whether writes are paused for this row/version. */
  function blocked(product: MembershipProduct): boolean {
    return pendingId.value !== null || !validVersion(product)
      || blockedVersions.value.get(product.id) === product.membership.version
  }

  /** @returns Nothing. Invalidate confirmations and in-flight feedback when the account changes. */
  function reset(): void {
    session++
    pendingId.value = null
    notice.value = ''
    blockedVersions.value.clear()
  }

  /**
   * Execute one version-guarded operation; refresh reads cannot automatically replay writes.
   * @param product Row captured before confirmation.
   * @param operation Delete requires confirmation; listing changes use the dedicated versioned endpoint.
   * @returns A promise resolving after confirmation, write and read-only refresh.
   */
  async function operate(product: MembershipProduct, operation: Operation): Promise<void> {
    const method = operation === 'remove' ? 'DELETE' : 'PATCH'
    if (blocked(product) || !can(method, product.id)) return
    if (operation === 'deactivate' && product.is_active !== true) return
    if (operation === 'activate' && product.is_active !== false) return
    const currentSession = session
    pendingId.value = product.id
    notice.value = ''
    try {
      if (operation === 'remove') {
        const confirmed = await confirmAction({
          description: t('telegramMembership.operations.deleteConfirm', { name: getLocalizedText(product.title), id: product.id }),
          confirmText: t('telegramMembership.operations.remove'), variant: 'destructive',
        })
        if (!confirmed || currentSession !== session || !can(method, product.id)) return
      }
      blockedVersions.value.set(product.id, product.membership.version)
      const response = operation === 'remove'
        ? await telegramMembershipAPI.remove(product.id, product.membership.version)
        : await telegramMembershipAPI.setActive(product.id, operation === 'activate', product.membership.version)
      if (currentSession !== session) return
      // A successful transport is not proof of a resulting listing state; read the authoritative list.
      if (!response?.data || response.data.status_code !== 0) {
        notifyErrorOnce(new Error('Invalid membership operation response'), t('common.api.responseMissing'))
        notice.value = t('telegramMembership.operations.unknown')
        return
      }
      const refreshed = await refresh()
      if (currentSession !== session) return
      notice.value = refreshed ? '' : t('telegramMembership.operations.refreshFailed')
    } catch (error: unknown) {
      if (currentSession !== session) return
      const failure = error as OperationError
      const unknown = isResultUnknown(failure) || failure?.outcome === 'unknown' || !failure?.response
      const conflict = failure?.response?.status === 409
      if (!unknown && !conflict) blockedVersions.value.delete(product.id)
      notifyErrorOnce(error, t('common.api.requestFailed'))
      // Retain only the safety decision; raw API diagnostics already belong to the toast.
      notice.value = unknown ? t('telegramMembership.operations.unknown')
        : conflict ? t('telegramMembership.operations.conflict') : ''
    } finally {
      if (currentSession === session) pendingId.value = null
    }
  }

  return { pendingId, notice, blocked, validVersion, operate, reset }
}
