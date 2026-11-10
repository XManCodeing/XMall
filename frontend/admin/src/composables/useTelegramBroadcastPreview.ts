import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { adminAPI, type TelegramBroadcastPayload } from '@/api/admin'

// Preview is explicit, never triggered by typing. Only the exact previewed draft can be sent.
export function useTelegramBroadcastPreview(
  payload: Ref<TelegramBroadcastPayload>,
  eligible: Ref<boolean>,
  audienceInputs: Ref<unknown>,
) {
  const open = ref(false)
  const loading = ref(false)
  const count = ref<number | null>(null)
  const state = ref<'idle' | 'loading' | 'ready' | 'failed' | 'stale'>('idle')
  let requestID = 0
  let snapshot: TelegramBroadcastPayload | null = null

  const invalidate = () => {
    ++requestID
    snapshot = null
    count.value = null
    loading.value = false
    state.value = open.value ? 'stale' : 'idle'
  }
  watch([payload, eligible, audienceInputs], invalidate, { deep: true, flush: 'sync' })
  watch(open, value => { if (!value) invalidate() }, { flush: 'sync' })
  onScopeDispose(invalidate)

  const canConfirm = computed(() => open.value && eligible.value && !loading.value
    && state.value === 'ready' && count.value !== null && count.value > 0)

  const preview = async () => {
    if (!eligible.value || loading.value) return
    open.value = true
    invalidate()
    const request = ++requestID
    const draft: TelegramBroadcastPayload = JSON.parse(JSON.stringify(payload.value))
    loading.value = true
    state.value = 'loading'
    try {
      const response = await adminAPI.previewTelegramBroadcast(draft)
      if (request !== requestID) return
      const recipientCount = response.data?.data?.recipient_count
      if (typeof recipientCount !== 'number' || !Number.isSafeInteger(recipientCount) || recipientCount < 0) {
        throw new Error('Invalid recipient count')
      }
      snapshot = draft
      count.value = recipientCount
      state.value = 'ready'
    } catch {
      if (request !== requestID) return
      snapshot = null
      count.value = null
      state.value = 'failed'
    } finally {
      if (request === requestID) loading.value = false
    }
  }

  const consume = () => {
    if (!canConfirm.value || !snapshot) return null
    const draft = snapshot
    invalidate()
    open.value = false
    return draft
  }

  return { open, loading, count, state, canConfirm, preview, consume }
}
