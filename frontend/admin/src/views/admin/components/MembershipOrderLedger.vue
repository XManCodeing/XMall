<script setup lang="ts">
import { Badge } from '@/components/ui/badge'
import { membershipStatusVariant } from '@/utils/membershipStatus'
import { useI18n } from 'vue-i18n'
import type { MembershipLedger } from '@/api/telegramMembership'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDate } from '@/utils/format'

defineProps<{ ledger?: MembershipLedger }>()
const { t } = useI18n()
/** Resolve only known persisted statuses; unknown values never become successful admission labels. */
function statusLabel(status: string): string {
  const known = ['pending', 'failed', 'recorded', 'active', 'revoked', 'expired', 'pending_restore']
  return t(`telegramMembership.ledger.${known.includes(status) ? status : 'unknown'}`)
}
</script>
<template>
  <section class="space-y-3 border-t border-border pt-3">
    <h4 class="font-medium">{{ t('telegramMembership.ledger.title') }}</h4>
    <p class="text-sm text-muted-foreground">{{ t('telegramMembership.ledger.hint') }}</p>
    <p v-if="!ledger" class="text-sm text-muted-foreground">{{ t('telegramMembership.ledger.empty') }}</p>
    <template v-else>
      <Badge :variant="membershipStatusVariant(ledger.state)">{{ statusLabel(ledger.state) }}</Badge>
      <p v-if="!ledger.sources.length" class="text-sm text-muted-foreground">{{ t('telegramMembership.ledger.empty') }}</p>
      <div v-else class="rounded-xl border border-border bg-card overflow-x-auto">
        <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow>
          <TableHead class="px-4 py-3">{{ t('telegramMembership.targets') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.identity') }}</TableHead>
          <TableHead class="px-4 py-3">{{ t('telegramMembership.term') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.ledger.paidAt') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.ledger.sourceStatus') }}</TableHead>
        </TableRow></TableHeader><TableBody class="divide-y divide-border"><TableRow v-for="(source, index) in ledger.sources" :key="`${source.target_id}:${index}`" class="hover:bg-muted/30">
          <TableCell class="px-4 py-3">{{ source.target_id }}</TableCell><TableCell class="px-4 py-3 font-mono">{{ source.beneficiary_telegram_id }}</TableCell>
          <TableCell class="px-4 py-3">{{ source.kind === 'permanent' ? t('telegramMembership.permanent') : `${source.months} ${t('telegramMembership.months')}` }}</TableCell>
          <TableCell class="px-4 py-3">{{ formatDate(source.paid_at) }}</TableCell><TableCell class="px-4 py-3"><Badge :variant="membershipStatusVariant(source.status)">{{ statusLabel(source.status) }}</Badge></TableCell>
        </TableRow></TableBody></Table>
      </div>
      <h5 class="text-sm font-medium">{{ t('telegramMembership.ledger.effective') }}</h5>
      <p v-if="!ledger.effective.length" class="text-sm text-muted-foreground">{{ t('telegramMembership.ledger.noEffective') }}</p>
      <div v-else class="rounded-xl border border-border bg-card overflow-x-auto">
        <Table><TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground"><TableRow><TableHead class="px-4 py-3">{{ t('telegramMembership.targets') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.identity') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.ledger.expiresAt') }}</TableHead><TableHead class="px-4 py-3">{{ t('telegramMembership.ledger.restore') }}</TableHead></TableRow></TableHeader>
          <TableBody class="divide-y divide-border"><TableRow v-for="effective in ledger.effective" :key="`${effective.target_id}:${effective.beneficiary_telegram_id}`" class="hover:bg-muted/30">
            <TableCell class="px-4 py-3">{{ effective.target_id }}</TableCell><TableCell class="px-4 py-3 font-mono">{{ effective.beneficiary_telegram_id }}</TableCell>
            <TableCell class="px-4 py-3">{{ effective.permanent ? t('telegramMembership.permanent') : effective.expires_at ? formatDate(effective.expires_at) : t('telegramMembership.ledger.noEffective') }}</TableCell>
            <TableCell class="px-4 py-3"><Badge :variant="membershipStatusVariant(effective.pending_restore ? 'pending_restore' : 'none')">{{ t(`telegramMembership.ledger.${effective.pending_restore ? 'pending_restore' : 'none'}`) }}</Badge></TableCell>
          </TableRow></TableBody>
        </Table>
      </div>
    </template>
  </section>
</template>
