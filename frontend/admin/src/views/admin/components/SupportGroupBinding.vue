<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { TelegramGroup } from '@/api/telegramGroups'
import { useTelegramGroupModules } from '@/composables/useTelegramGroupModules'
import { groupReady } from '@/composables/useTelegramGroups'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog'

const props = defineProps<{ group: TelegramGroup; now: number; revision: number; disabled?: boolean }>()
const emit = defineEmits<{ busy: [value: boolean]; saved: [] }>()
const { t } = useI18n()
const { rows, loading, error, busy, errors, conflicts, canRead, canWrite, select, save } = useTelegramGroupModules()
const binding = computed(() => rows.value.find(row => row.partition === 'support'))
const pendingActive = ref<boolean | null>(null)
const active = computed(() => pendingActive.value ?? (binding.value?.target_id === props.group.id && binding.value.configured_enabled))
const confirmation = ref(false)
const requestedEnabled = ref(false)
const confirmationAction = computed(() => !requestedEnabled.value ? 'receptionStop'
  : binding.value?.target_id && binding.value.target_id !== props.group.id ? 'receptionReplace' : 'receptionStart')
const confirmationMessage = computed(() => confirmationAction.value === 'receptionReplace' ? 'receptionConfirm' : `${confirmationAction.value}Confirm`)
const reason = computed(() => {
  if (!canRead.value || !canWrite.value) return t('telegramBot.groups.forbidden')
  if (error.value || errors.value.support) return error.value || errors.value.support
  if (loading.value) return t('telegramBot.instances.loading')
  if (!binding.value) return t('telegramBot.groups.failed')
  if (props.group.recovery_task_id) return t('telegramSupportSettings.receptionRecovery')
  // Stale capability evidence must not prevent stopping an existing binding.
  if (!active.value && !props.group.enabled) return t('telegramSupportSettings.receptionEnableGroup')
  if (!active.value && !groupReady(props.group, props.now)) return t('telegramSupportSettings.receptionCheckGroup')
  return ''
})
const blocked = computed(() => props.disabled || !!busy.value || !!reason.value || !!conflicts.value.support)
watch(busy, value => emit('busy', !!value), { flush: 'sync' })

/** Reload saved support intent for this row's Bot. @returns Completion promise. */
async function reload() {
  confirmation.value = false
  await select(String(props.group.bot_instance_id))
}
watch(() => [props.group, props.revision], () => { void reload() }, { immediate: true })

/** Persist only support intent, restoring the saved switch on failure.
 * @param enabled Desired reception state. @returns Completion promise.
 */
async function persist(enabled: boolean) {
  if (!confirmation.value || enabled !== requestedEnabled.value || blocked.value || !binding.value) return
  const row = binding.value
  const previous = { ...row }
  pendingActive.value = !!active.value
  row.target_id = props.group.id
  row.configured_enabled = enabled
  await save(row)
  if (binding.value?.version !== previous.version) {
    confirmation.value = false
    emit('saved')
  } else {
    Object.assign(row, previous)
  }
  pendingActive.value = null
}

/** Request confirmation before every reception change without changing saved intent.
 * @param enabled Requested switch state. @returns Nothing.
 */
function toggle(enabled: boolean) {
  if (blocked.value || !binding.value || enabled === !!active.value) return
  requestedEnabled.value = enabled
  confirmation.value = true
}
</script>

<template>
  <div class="min-w-28 max-w-56 space-y-1">
    <Switch :model-value="!!active" :disabled="!!blocked" :aria-label="`${t('telegramSupportSettings.reception')} · ${group.name}`" :aria-busy="!!busy" @update:model-value="toggle" />
    <p v-if="reason" :role="error || errors.support ? 'alert' : undefined" class="text-xs text-muted-foreground">{{ reason }}</p>
    <p v-else-if="active && !binding?.effective_enabled" class="text-xs text-muted-foreground">{{ t('telegramSupportSettings.receptionUnavailable') }}</p>
    <Button v-if="error || errors.support || (!loading && canRead && !binding)" size="sm" variant="ghost" :disabled="!!busy || disabled" @click="reload">{{ t('telegramBot.groups.reloadDraft') }}</Button>
  </div>
  <Dialog :open="confirmation" @update:open="value => { if (!busy) confirmation = value }">
    <DialogContent>
      <DialogHeader><DialogTitle>{{ t(`telegramSupportSettings.${confirmationAction}`) }}</DialogTitle></DialogHeader>
      <DialogDescription>{{ t(`telegramSupportSettings.${confirmationMessage}`, { name: group.name, id: binding?.target_id }) }}</DialogDescription>
      <p v-if="errors.support" role="alert" class="text-sm text-destructive">{{ errors.support }}</p>
      <DialogFooter>
        <Button variant="outline" :disabled="!!busy" @click="confirmation = false">{{ t('admin.common.cancel') }}</Button>
        <Button :disabled="!!blocked" @click="persist(requestedEnabled)">{{ t(`telegramSupportSettings.${confirmationAction}`) }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
