import { computed, onUnmounted, reactive, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { supportBase, supportPositive, supportResolvePermission, telegramSupportAPI, validSupportResolution, validSupportTopicResolution, type SupportTaskRecord, type SupportTaskResource, type SupportResolution, type SupportTopicResolution } from '@/api/telegramSupport'

/** Reconcile independent task identities; resource/bot changes invalidate all requests and drafts. */
export function useTelegramSupportTask(reload: () => Promise<void>, resource: MaybeRefOrGetter<SupportTaskResource> = 'downstreams', bot: MaybeRefOrGetter<string> = '') {
  const { t } = useI18n(), auth = useAdminAuthStore()
  const selected = computed(() => toValue(resource)), isTopic = computed(() => selected.value === 'topic-operations')
  const open = ref(false), loading = ref(false), saving = ref(false), error = ref(''), success = ref('')
  const task = ref<SupportTaskRecord | null>(null), conflict = ref(false)
  const form = reactive({ decision: '' as '' | 'delivered' | 'failed' | 'succeeded', evidence: '', deliveryID: '', threadID: '', noEffect: false })
  const canRead = computed(() => !!task.value && auth.hasPermission(`GET:${supportBase}/${selected.value}/${task.value.id}`))
  // Every resolver currently calls the backend's unified exact downstream resolution authorizer.
  const canResolve = computed(() => canRead.value && auth.hasPermission(`POST:${supportBase}/${selected.value}/${task.value!.id}/resolve`) && (auth.isSuper || auth.permissions.includes(supportResolvePermission)))
  const input = computed<SupportResolution>(() => ({ expected_version: task.value?.version ?? 0, decision: form.decision as SupportResolution['decision'], evidence: form.evidence, delivery_id: Number(form.deliveryID) }))
  const topicInput = computed<SupportTopicResolution>(() => ({ expected_version: task.value?.version ?? 0, decision: form.decision as SupportTopicResolution['decision'], evidence: form.evidence, thread_id: form.threadID, no_effect: form.noEffect }))
  const reviewable = computed(() => task.value?.state === 'unknown' && supportPositive(task.value.version))
  const valid = computed(() => reviewable.value && canResolve.value && !conflict.value && (isTopic.value ? validSupportTopicResolution(topicInput.value) : /^[1-9]\d*$/.test(form.deliveryID) && validSupportResolution(input.value)))
  let request = 0, operation = 0
  /** Clear sensitive review drafts, including explicit no-effect confirmation. */
  function clearDraft() { Object.assign(form, { decision: '', evidence: '', deliveryID: '', threadID: '', noEffect: false }) }
  /** Invalidate both reads and writes without allowing late completions to affect a new resource. */
  function reset() { request++; operation++; task.value = null; loading.value = false; saving.value = false; error.value = ''; success.value = ''; conflict.value = false; clearDraft() }
  /** Fetch metadata by the current namespace's ID, optionally preserving the conflict barrier. */
  async function show(id: number, keepConflict = false) {
    const current = ++request, kind = selected.value, botID = toValue(bot)
    task.value = null; error.value = ''; success.value = ''; loading.value = false; open.value = true
    clearDraft(); conflict.value = keepConflict
    if (!supportPositive(id) || !auth.hasPermission(`GET:${supportBase}/${kind}/${id}`)) return
    loading.value = true
    try {
      const fetch = { downstreams: telegramSupportAPI.task, upstreams: telegramSupportAPI.upstream, 'topic-operations': telegramSupportAPI.topic }[kind]
      const response = await fetch(id)
      if (current !== request || !open.value || !auth.hasPermission(`GET:${supportBase}/${kind}/${id}`)) return
      const row = response.data.data as SupportTaskRecord
      if (row.id !== id || (botID && String(row.bot_id) !== botID)) { error.value = t('telegramSupport.failed'); return }
      task.value = row
      form.deliveryID = 'delivery_id' in row && row.delivery_id ? String(row.delivery_id) : ''
    } catch { if (current === request) error.value = t('telegramSupport.failed') }
    finally { if (current === request) loading.value = false }
  }
  /** Submit one reviewed result; a 409 reloads this namespace and clears conclusions, never retries. */
  async function resolve() {
    if (!task.value || !valid.value || saving.value || loading.value) return
    const id = task.value.id, kind = selected.value, current = request, active = ++operation
    saving.value = true; error.value = ''; success.value = ''
    try {
      if (kind === 'topic-operations') await telegramSupportAPI.resolveTopic(id, topicInput.value)
      else await (kind === 'upstreams' ? telegramSupportAPI.resolveUpstream : telegramSupportAPI.resolve)(id, input.value)
      if (active !== operation || current !== request || !open.value) return
      const refresh = request + 1
      await show(id)
      if (active !== operation || request !== refresh || !open.value) return
      success.value = t('telegramSupport.resolved'); await reload()
    } catch (failure: unknown) {
      if (active !== operation || current !== request || !open.value) return
      const status = (failure as { response?: { status?: number } })?.response?.status
      if (status === 409) {
        const refresh = request + 1
        await show(id, true)
        if (active === operation && request === refresh && open.value) await reload()
      } else error.value = t('telegramSupport.failed')
    } finally { if (active === operation) saving.value = false }
  }
  /** Require a new explicit review after a CAS conflict. */
  function acknowledgeConflict() { if (task.value && !loading.value && !saving.value) conflict.value = false }
  watch([selected, () => toValue(bot)], () => { reset(); open.value = false }, { flush: 'sync' })
  watch(open, value => { if (!value) reset() }, { flush: 'sync' })
  watch(canRead, allowed => { if (!allowed && task.value) reset() }, { flush: 'sync' })
  watch(canResolve, allowed => { if (!allowed) clearDraft() }, { flush: 'sync' })
  watch(() => form.decision, () => { form.threadID = ''; form.noEffect = false }, { flush: 'sync' })
  onUnmounted(reset)
  return { open, loading, saving, error, success, task, conflict, form, canResolve, reviewable, valid, isTopic, show, resolve, acknowledgeConflict }
}
