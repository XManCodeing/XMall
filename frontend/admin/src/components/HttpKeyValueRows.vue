<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { KeyValueRow } from '@/utils/httpIntegration'

defineProps<{ label: string; secret?: boolean }>()
const rows = defineModel<KeyValueRow[]>({ required: true })
const { t } = useI18n()
</script>

<template>
  <div class="space-y-2">
    <div class="flex items-center justify-between gap-2">
      <h3 class="text-sm font-medium">{{ label }}</h3>
      <Button type="button" size="sm" variant="outline" @click="rows.push({ key: '', value: '' })">{{ t('httpIntegration.add') }}</Button>
    </div>
    <p v-if="!rows.length" class="text-xs text-muted-foreground">{{ t('httpIntegration.emptyRows') }}</p>
    <div v-for="(row, index) in rows" :key="index" class="flex items-center gap-2">
      <Input v-model="row.key" class="h-9 min-w-0 flex-1" :aria-label="`${label}: ${t('httpIntegration.key')}`" :placeholder="t('httpIntegration.key')" autocomplete="off" />
      <Input v-model="row.value" class="h-9 min-w-0 flex-1" :type="secret ? 'password' : 'text'" :aria-label="`${label}: ${t('httpIntegration.value')}`" :placeholder="row.retained !== undefined ? t('httpIntegration.configuredSecret') : t('httpIntegration.value')" :autocomplete="secret ? 'new-password' : 'off'" />
      <Button type="button" size="sm" variant="ghost" @click="rows.splice(index, 1)">{{ t('httpIntegration.remove') }}</Button>
    </div>
  </div>
</template>
