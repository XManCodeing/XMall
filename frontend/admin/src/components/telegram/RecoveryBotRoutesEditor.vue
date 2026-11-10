<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { useRecoveryBotRoutes } from '@/composables/useRecoveryBotRoutes'
import { recoveryBotRouteMessages } from '@/i18n/recoveryBotRoutes'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
defineProps<{ model: ReturnType<typeof useRecoveryBotRoutes>; allowed: boolean; source: string; disabled: boolean }>()
const { t } = useI18n({ useScope: 'local', messages: recoveryBotRouteMessages })
</script>
<template>
  <section class="space-y-3 border-t border-border pt-4">
    <div class="flex items-center justify-between gap-3"><h3 class="font-medium">{{ t('title') }}</h3><Button type="button" size="sm" variant="outline" :disabled="disabled || !allowed || !source || model.loading.value" @click="model.load()">{{ t('load') }}</Button></div>
    <p class="text-xs text-muted-foreground">{{ t('hint') }}</p><p class="text-xs text-muted-foreground">{{ t('invalidated') }}</p>
    <p v-if="!allowed" class="text-sm text-muted-foreground">{{ t('forbidden') }}</p>
    <p v-if="model.loading.value" role="status">{{ t('loading') }}</p><p v-if="model.error.value" role="alert" class="text-sm text-destructive">{{ t(model.error.value) }}</p>
    <div v-if="allowed" class="space-y-2"><Label for="bot-route-target">{{ t('target') }}</Label><Select :model-value="''" :disabled="disabled || model.loading.value || model.routes.value.length >= 100" @update:model-value="value => model.add(String(value))"><SelectTrigger id="bot-route-target"><SelectValue :placeholder="t('add')" /></SelectTrigger><SelectContent><SelectItem v-for="option in model.options.value" :key="option.id" :value="option.id" :disabled="model.routes.value.some(route => route.target_id === option.id)">{{ option.name }} · {{ option.id }} · {{ option.chat }}</SelectItem></SelectContent></Select>
      <p v-if="!model.options.value.length && !model.loading.value" class="text-xs text-muted-foreground">{{ t('empty') }}</p>
      <div class="flex items-center justify-end gap-3"><Button type="button" size="sm" variant="outline" :disabled="disabled || model.loading.value || model.page.value <= 1" @click="model.load(model.page.value - 1)">{{ t('previous') }}</Button><span class="text-xs text-muted-foreground">{{ t('page', { page: model.page.value }) }}</span><Button type="button" size="sm" variant="outline" :disabled="disabled || model.loading.value || model.page.value * 20 >= model.total.value" @click="model.load(model.page.value + 1)">{{ t('next') }}</Button></div>
    </div>
    <div v-if="model.routes.value.length" class="rounded-xl border border-border bg-card overflow-x-auto">
      <Table>
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="px-4 py-3">{{ t('target') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('old') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('destination') }}</TableHead>
            <TableHead class="px-4 py-3 text-right" />
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-for="route in model.routes.value" :key="route.target_id" class="hover:bg-muted/30">
            <TableCell class="px-4 py-3">{{ route.target_id }}</TableCell>
            <TableCell class="px-4 py-3">{{ model.selectedOptions.value.find(option => option.id === route.target_id)?.chat }}</TableCell>
            <TableCell class="px-4 py-3"><Input v-model="route.chat_id" :aria-label="`${t('destination')} · ${route.target_id}`" type="text" maxlength="20" :disabled="disabled || model.loading.value" autocomplete="off" /></TableCell>
            <TableCell class="px-4 py-3 text-right">
              <div class="flex flex-wrap items-center justify-end gap-2">
                <Button type="button" size="sm" variant="outline" :disabled="disabled" @click="model.remove(route.target_id)">{{ t('remove') }}</Button>
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>
    <p v-if="model.routes.value.length && !model.valid.value && !model.loading.value" role="alert" class="text-sm text-destructive">{{ t('invalid') }}</p>
  </section>
</template>
