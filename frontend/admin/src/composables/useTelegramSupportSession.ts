import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { supportBase, supportContentPermission, supportPositive, telegramSupportAPI, type SupportSession, type SupportMessage, type SupportContent } from '@/api/telegramSupport'

/** Isolate metadata and audited content, with no persistent cache or automatic body reads. */
export function useTelegramSupportSession() {
  const { t } = useI18n(), auth = useAdminAuthStore()
  const open = ref(false), loading = ref(false), error = ref('')
  const session = ref<SupportSession | null>(null), messages = ref<SupportMessage[]>([])
  const page = ref(1), pageSize = ref(20), total = ref(0), messageLoading = ref(false), messageError = ref('')
  const content = ref<SupportContent | null>(null), contentID = ref<number | null>(null), contentLoading = ref(false), contentError = ref('')
  const canMessages = computed(() => !!session.value && auth.hasPermission(`GET:${supportBase}/sessions/${session.value.id}/messages`))
  // Wildcard metadata grants deliberately do not imply permission to decrypt bodies.
  const canContent = computed(() => auth.isSuper || auth.permissions.includes(supportContentPermission))
  let detailRequest = 0, messageRequest = 0, contentRequest = 0
  let expiryTimer: ReturnType<typeof setTimeout> | undefined
  /** Clear sensitive display and invalidate pending responses whenever scope changes. */
  function clearContent() {
    contentRequest++; content.value = null; contentID.value = null; contentError.value = ''; contentLoading.value = false
    clearTimeout(expiryTimer)
  }
  /** Load one session's metadata, then the separately authorized message metadata page. */
  async function show(id: number) {
    const current = ++detailRequest
    messageRequest++; clearContent(); session.value = null; messages.value = []; total.value = 0; error.value = ''; messageError.value = ''
    loading.value = false; messageLoading.value = false; open.value = true
    if (!supportPositive(id) || !auth.hasPermission(`GET:${supportBase}/sessions/${id}`)) return
    loading.value = true
    try {
      const response = await telegramSupportAPI.session(id)
      if (current !== detailRequest || !open.value || !auth.hasPermission(`GET:${supportBase}/sessions/${id}`)) return
      session.value = response.data.data
      await loadMessages(1)
    } catch { if (current === detailRequest) error.value = t('telegramSupport.failed') }
    finally { if (current === detailRequest) loading.value = false }
  }
  /** Page message metadata only; changing page discards any previously viewed body. */
  async function loadMessages(nextPage = page.value) {
    const current = ++messageRequest, id = session.value?.id
    clearContent(); messages.value = []; total.value = 0; messageError.value = ''; messageLoading.value = false
    if (!id || !canMessages.value || !supportPositive(nextPage) || ![10, 20, 50, 100].includes(pageSize.value)) return
    messageLoading.value = true
    try {
      const response = await telegramSupportAPI.messages(id, { page: nextPage, page_size: pageSize.value })
      if (current !== messageRequest || !open.value || !canMessages.value) return
      messages.value = response.data.data.items; total.value = response.data.data.total; page.value = nextPage
    } catch { if (current === messageRequest) messageError.value = t('telegramSupport.failed') }
    finally { if (current === messageRequest) messageLoading.value = false }
  }
  /** Explicit user action invokes server exact authorization and durable audit; text is never logged. */
  async function viewContent(message: SupportMessage) {
    clearContent()
    const id = session.value?.id, current = contentRequest
    if (!id || !canContent.value || !open.value || !messages.value.some(row => row.id === message.id)) return
    contentID.value = message.id; contentLoading.value = true
    try {
      const response = await telegramSupportAPI.content(id, message.id)
      if (current !== contentRequest || !open.value || !canContent.value) return
      const remaining = Date.parse(message.retain_until) - Date.now()
      content.value = response.data.data
      // Fail closed for malformed retention timestamps and erase text at the retention boundary.
      if (content.value?.available) {
        if (!Number.isFinite(remaining) || remaining <= 0) content.value = { available: false, reason: 'expired' }
        else expiryTimer = setTimeout(() => { content.value = { available: false, reason: 'expired' } }, Math.min(remaining, 2147483647))
      }
    } catch { if (current === contentRequest) contentError.value = t('telegramSupport.failed') }
    finally { if (current === contentRequest) contentLoading.value = false }
  }
  watch(open, value => { if (!value) { detailRequest++; messageRequest++; clearContent(); session.value = null; messages.value = [] } })
  watch(canContent, value => { if (!value) clearContent() })
  onUnmounted(() => { detailRequest++; messageRequest++; clearContent() })
  return { open, loading, error, session, messages, page, pageSize, total, messageLoading, messageError, canMessages, canContent, content, contentID, contentLoading, contentError, show, loadMessages, viewContent, clearContent }
}
