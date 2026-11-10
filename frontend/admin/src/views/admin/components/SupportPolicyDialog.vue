<script setup lang="ts">
import { watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTelegramSupportSettings } from '@/composables/useTelegramSupportSettings'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter } from '@/components/ui/dialog'

const props = defineProps<{ botID: number; botLabel: string }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const { snapshot, draft, loading, saving, error, canRead, canWrite, canSave, load, select, defaults, save } = useTelegramSupportSettings()
const fields = [
  { key: 'max_messages', min: 1, max: 100 },
  { key: 'max_bytes', min: 1024, max: 4194304 },
  { key: 'max_attempts', min: 1, max: 10 },
  { key: 'retention_seconds', min: 60, max: 604800 },
  { key: 'challenge_ttl_seconds', min: 60, max: 3600 },
  { key: 'cooldown_seconds', min: 60, max: 3600 },
] as const
watch(() => props.botID, value => { void select(String(value)) }, { immediate: true })

/** Keep the dialog mounted while saving so the response retains its Bot scope.
 * @param open Requested dialog visibility. @returns Nothing.
 */
function changeOpen(open: boolean) {
  if (!open && !saving.value) emit('close')
}
</script>

<template>
  <Dialog scope="page" :open="true" @update:open="changeOpen">
    <DialogScrollContent class="sm:max-w-2xl" @escape-key-down="saving && $event.preventDefault()" @interact-outside="saving && $event.preventDefault()">
      <DialogHeader>
        <DialogTitle>{{ t('telegramSupportSettings.dialogTitle') }} · {{ botLabel }}</DialogTitle>
        <DialogDescription class="space-y-2">
          <span class="block">{{ t('telegramSupportSettings.purposeHint') }}</span>
          <span class="block">{{ t('telegramSupportSettings.sharedHint') }}{{ t('telegramSupportSettings.scopeHint') }}</span>
        </DialogDescription>
      </DialogHeader>
      <p v-if="!canRead" class="text-sm text-muted-foreground">{{ t('telegramBot.groups.forbidden') }}</p>
      <p v-if="loading" role="status" class="text-sm text-muted-foreground">{{ t('telegramBot.instances.loading') }}</p>
      <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
      <template v-if="snapshot && draft && canRead">
        <div class="flex flex-wrap items-center gap-3 text-sm">
          <Badge :variant="membershipStatusVariant(snapshot.production_ready ? 'ready' : 'inactive')">{{ t(snapshot.production_ready ? 'telegramSupportSettings.ready' : 'telegramSupportSettings.closed') }}</Badge>
          <span class="text-muted-foreground">{{ t('telegramSupportSettings.version', { version: snapshot.version }) }}</span>
        </div>
        <h3 class="font-medium">{{ t('telegramSupportSettings.policyTitle') }}</h3>
        <fieldset :disabled="!canWrite || loading || saving" class="grid grid-cols-2 gap-5">
          <div v-for="field in fields" :key="field.key" class="space-y-2">
            <Label :for="`support-${field.key}`">{{ t(`telegramSupportSettings.${field.key}`) }}</Label>
            <Input :id="`support-${field.key}`" v-model.number="draft[field.key]" type="number" :min="field.min" :max="field.max" step="1" />
            <p class="text-xs text-muted-foreground">{{ t('telegramSupportSettings.range', { min: field.min, max: field.max }) }}</p>
          </div>
        </fieldset>
        <div class="flex items-center justify-between gap-3">
          <p class="text-sm text-muted-foreground">{{ t('telegramSupportSettings.retentionHint') }}</p>
          <Button v-if="canWrite" variant="ghost" :disabled="loading || saving" @click="defaults">{{ t('telegramSupportSettings.defaults') }}</Button>
        </div>
      </template>
      <DialogFooter>
        <Button v-if="canRead" variant="outline" :disabled="loading || saving" @click="load()">{{ t('telegramSupportSettings.policyReload') }}</Button>
        <Button variant="outline" :disabled="saving" @click="changeOpen(false)">{{ t('admin.common.cancel') }}</Button>
        <Button v-if="canWrite" :disabled="!canSave" @click="save">{{ t(saving ? 'telegramBot.instances.loading' : 'telegramSupportSettings.policySave') }}</Button>
      </DialogFooter>
    </DialogScrollContent>
  </Dialog>
</template>
