<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { RecoveryBotRoute } from '@/api/telegramRecovery'
import { recoveryBotRouteMessages } from '@/i18n/recoveryBotRoutes'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
defineProps<{ routes: RecoveryBotRoute[] }>()
const { t } = useI18n({ useScope: 'local', messages: recoveryBotRouteMessages })
/** Mirror immutable DestinationChat fallback, not mutable current groups. @param route Saved route. @returns Saved destination chat. */
function destination(route: RecoveryBotRoute) { return route.new_chat_id && route.new_chat_id !== '0' ? route.new_chat_id : route.chat_id }
</script>
<template>
  <section class="space-y-2">
    <h3 class="font-medium">{{ t('snapshot') }}</h3>
    <div class="rounded-xl border border-border bg-card overflow-x-auto">
      <Table>
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="px-4 py-3">{{ t('target') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('purpose') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('old') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('destination') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('version') }}</TableHead>
            <TableHead class="px-4 py-3">{{ t('routeVersion') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-for="route in routes" :key="route.target_id" class="hover:bg-muted/30">
            <TableCell class="px-4 py-3">{{ route.target_id }}</TableCell>
            <TableCell class="px-4 py-3">{{ t(['support', 'membership'].includes(route.purpose) ? route.purpose : 'unknown') }}</TableCell>
            <TableCell class="px-4 py-3 font-mono">{{ route.chat_id }}</TableCell>
            <TableCell class="px-4 py-3 font-mono">{{ destination(route) }}</TableCell>
            <TableCell class="px-4 py-3">{{ route.version }}</TableCell>
            <TableCell class="px-4 py-3">{{ route.route_version }}</TableCell>
          </TableRow>
        </TableBody>
      </Table>
    </div>
  </section>
</template>
