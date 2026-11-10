import { computed, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAdminAuthStore } from '@/stores/auth'
import { telegramGroupsAPI, type GroupModule, type GroupPurpose } from '@/api/telegramGroups'
import { groupErrorKey } from './useTelegramGroups'
import { notifySuccess } from '@/utils/notify'

export function useTelegramGroupModules() {
  const { t } = useI18n()
  const auth = useAdminAuthStore()
  const open = ref(false)
  const botID = ref('')
  const loading = ref(false)
  const error = ref('')
  const rows = ref<GroupModule[]>([])
  const busy = ref<GroupPurpose | null>(null)
  const conflicts = ref<Partial<Record<GroupPurpose, boolean>>>({})
  const errors = ref<Partial<Record<GroupPurpose, string>>>({})
  const canRead = computed(() => !!botID.value && auth.hasPermission(`GET:/admin/telegram-bot/instances/${botID.value}/modules`))
  const canWrite = computed(() => !!botID.value && auth.hasPermission(`PATCH:/admin/telegram-bot/instances/${botID.value}/modules`))
  let request = 0
  onScopeDispose(() => { ++request })
  async function load() {
    if (busy.value) return
    const current = ++request
    rows.value = []; errors.value = {}; conflicts.value = {}; error.value = ''
    if (!canRead.value) { loading.value = false; return }
    loading.value = true
    try {
      const res = await telegramGroupsAPI.modules(Number(botID.value))
      if (current === request) rows.value = res.data.data.items ?? []
    } catch (err) { if (current === request) error.value = t(`telegramBot.groups.${groupErrorKey(err)}`) }
    finally { if (current === request) loading.value = false }
  }
  async function select(value: string) { if (busy.value) return; botID.value = value; await load() }
  async function save(row: GroupModule) {
    if (busy.value || loading.value || !botID.value || !auth.hasPermission(`PATCH:/admin/telegram-bot/instances/${botID.value}/modules`) || conflicts.value[row.partition] || !rows.value.includes(row)
      || !Number.isSafeInteger(row.target_id) || row.target_id < 0) return
    const current = request
    const bot = Number(botID.value)
    busy.value = row.partition; errors.value[row.partition] = ''
    try {
      const res = await telegramGroupsAPI.saveModule(bot, { partition: row.partition, expected_version: row.version, enabled: row.configured_enabled, target_id: row.target_id })
      if (current !== request) return
      // The response contains both partitions. Only replace the saved one; preserve the other draft.
      const saved = (res.data.data.items as GroupModule[]).find(item => item.partition === row.partition)
      if (saved) rows.value = rows.value.map(item => item.partition === row.partition ? saved : item)
      notifySuccess(t('telegramBot.groups.saved'))
    } catch (err) {
      if (current === request) {
        errors.value[row.partition] = t(`telegramBot.groups.${groupErrorKey(err)}`)
        conflicts.value[row.partition] = groupErrorKey(err) === 'conflict'
      }
    } finally { if (current === request) busy.value = null }
  }
  return { open, botID, loading, error, rows, busy, conflicts, errors, canRead, canWrite, load, select, save }
}
