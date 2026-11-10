import { computed, onScopeDispose, ref, watch, type Ref } from 'vue'
import { adminAPI } from '@/api/admin'
import { useAdminAuthStore } from '@/stores/auth'

const CHECK_INTERVAL = 60 * 60 * 1000

/** Check versions silently while authorized; never starts an upgrade.
 * @param dialogOpen Skip background requests while the manual checker is open.
 * @returns Update indicator and a receiver for manual check results.
 */
export function useSystemUpdateNotice(dialogOpen: Ref<boolean>) {
  const auth = useAdminAuthStore()
  const hasUpdate = ref(false)
  const latestVersion = ref('')
  const canCheck = computed(() => !!auth.token && auth.hasPermission('GET:/admin/system/version/check'))
  let timer: ReturnType<typeof setInterval> | undefined
  let generation = 0
  let pending = false

  /** Apply only a valid server result. @param result Version-check response. @returns Nothing. */
  function accept(result: { has_update: boolean; latest_version: string }) {
    if (!canCheck.value || typeof result?.has_update !== 'boolean' || typeof result.latest_version !== 'string') return
    hasUpdate.value = result.has_update
    latestVersion.value = result.latest_version
  }

  /** Perform one bounded read; retain last known result on transient failures. @returns Completion promise. */
  async function check() {
    if (!canCheck.value || pending || dialogOpen.value) return
    const current = generation
    pending = true
    try {
      const response = await adminAPI.checkSystemUpdate(undefined, { silent: true })
      if (current === generation && !dialogOpen.value) accept(response.data?.data)
    } catch {
      // The next hourly tick retries. Never toast or erase a known update on network failure.
      if (current === generation) console.warn('[system-update] Background version check failed; retrying next hour.')
    } finally {
      if (current === generation) pending = false
    }
  }

  /** Cancel scheduling and invalidate stale account responses. @returns Nothing. */
  function stop() {
    generation++
    pending = false
    clearInterval(timer)
    timer = undefined
  }
  watch(() => [auth.token, canCheck.value], () => {
    stop(); hasUpdate.value = false; latestVersion.value = ''
    if (!canCheck.value) return
    void check()
    timer = setInterval(() => { void check() }, CHECK_INTERVAL)
  }, { immediate: true, flush: 'sync' })
  // A manual check supersedes any background response already in flight.
  watch(dialogOpen, open => { if (open) { generation++; pending = false } }, { flush: 'sync' })
  onScopeDispose(stop)
  return { hasUpdate, latestVersion, canCheck, accept }
}
