<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ExternalLink } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import EmptyState from '@/components/EmptyState.vue'
import { useRechargeOrderDetail } from '@/composables/useRechargeOrderDetail'

const { t } = useI18n()
const {
  loading, checkingPayment, recharge, payment, walletAddressCopied, qrImageUrl,
  isPending, payLink, showTelegramPayHint, qrUsingPayLinkFallback, showQRCode,
  cryptoWalletAddress, cryptoPaymentDetails, hasCryptoPaymentDetails, customerFeeApplied,
  rechargeStatusText, rechargeStatusVariant, formatMoney, formatDate,
  loadDetail, checkPayment, handleOpenPayLink, handleCopyWalletAddress,
} = useRechargeOrderDetail()
</script>

<template>
  <div class="orbit-page orbit-page--recharge-order-detail mx-auto max-w-5xl px-4 py-8 sm:px-6 lg:py-12">
    <header class="mb-8 flex flex-wrap items-center justify-between gap-4">
      <div><h1 class="text-3xl font-semibold tracking-tight">{{ t('rechargeOrder.title') }}</h1><p class="mt-2 text-sm text-muted-foreground">{{ t('rechargeOrder.subtitle') }}</p></div>
      <router-link to="/me/orders" class="flex min-h-11 items-center text-sm text-primary">{{ t('rechargeOrder.backList') }}</router-link>
    </header>
    <div v-if="loading" role="status" class="theme-skeleton h-40 rounded-[20px]"><span class="sr-only">{{ t('common.loading') }}</span></div>
    <EmptyState v-else-if="!recharge" icon="alert" :title="t('rechargeOrder.notFound')" :action-label="t('errorBoundary.retry')" @action="loadDetail()" />
    <div v-else class="space-y-6" :class="{ 'orbit-recharge-content': isPending && payLink }">
      <section class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div class="min-w-0"><p class="text-xs text-muted-foreground">{{ t('personalCenter.wallet.rechargeNoLabel') }}</p><p class="mt-2 break-all text-sm font-medium">{{ recharge.recharge_no }}</p></div>
          <Badge :variant="rechargeStatusVariant(recharge.status)">{{ rechargeStatusText(recharge.status) }}</Badge>
        </div>
        <h2 class="mt-6 text-lg font-semibold">{{ t('rechargeOrder.amountTitle') }}</h2>
        <dl class="mt-4 space-y-3 text-sm">
          <div class="flex justify-between gap-4"><dt class="text-muted-foreground">{{ t('rechargeOrder.rechargeAmount') }}</dt><dd class="tabular-nums">{{ formatMoney(recharge.amount, recharge.currency) }}</dd></div>
          <div v-if="customerFeeApplied" class="flex justify-between gap-4 text-warning"><dt>{{ t('payment.feeAmountLabel') }}</dt><dd class="tabular-nums">{{ formatMoney(recharge.fee_amount, recharge.currency) }}</dd></div>
          <div class="flex justify-between gap-4 border-t pt-4 font-semibold"><dt>{{ t('personalCenter.wallet.payAmountLabel') }}</dt><dd class="tabular-nums">{{ formatMoney(recharge.payable_amount, recharge.currency) }}</dd></div>
        </dl>
      </section>
      <section v-if="isPending" class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
        <h2 class="text-lg font-semibold">{{ t('rechargeOrder.paymentTitle') }}</h2>
        <p class="mt-2 text-sm text-muted-foreground">{{ t('personalCenter.wallet.pendingHint') }}</p>
        <div class="mt-5 grid gap-6 md:grid-cols-2">
          <div v-if="showQRCode" class="space-y-3 text-center">
            <p class="text-sm font-medium">{{ t('payment.qrTitle') }}</p>
            <div class="mx-auto w-full max-w-[260px] rounded-xl bg-white p-2"><img :src="qrImageUrl" :alt="t('payment.qrTitle')" class="aspect-square w-full object-contain" /></div>
            <p v-if="qrUsingPayLinkFallback" class="text-xs text-muted-foreground">{{ t('payment.qrFallbackHint') }}</p>
          </div>
          <div class="space-y-4">
            <div v-if="hasCryptoPaymentDetails" class="space-y-3 rounded-xl bg-secondary p-4">
              <div v-for="item in cryptoPaymentDetails" :key="item.key" class="space-y-1"><p class="text-xs text-muted-foreground">{{ item.label }}</p><p class="break-all text-sm font-medium">{{ item.value }} <span v-if="item.detail" class="font-normal text-muted-foreground">({{ item.detail }})</span></p></div>
              <div v-if="cryptoWalletAddress" class="flex flex-wrap items-center gap-2"><Button variant="outline" class="min-h-11 rounded-xl" @click="handleCopyWalletAddress">{{ t('payment.copyWalletAddress') }}</Button><span v-if="walletAddressCopied" role="status" class="text-xs text-success">{{ t('payment.copied') }}</span></div>
            </div>
            <div v-if="payLink" class="orbit-mobile-action"><Button type="button" variant="default" class="h-12 w-full rounded-xl" @click="handleOpenPayLink"><ExternalLink class="h-4 w-4" />{{ t('payment.openPayLink') }}</Button></div>
            <Button variant="outline" class="min-h-11 w-full rounded-xl" :disabled="checkingPayment" @click="checkPayment">{{ checkingPayment ? t('personalCenter.wallet.checkingPayStatus') : t('personalCenter.wallet.checkPayStatus') }}</Button>
            <p v-if="showTelegramPayHint" class="text-xs text-muted-foreground">{{ t('payment.telegramExternalHint') }}</p>
          </div>
        </div>
      </section>
      <p v-if="recharge.status === 'success'" role="status" class="rounded-[20px] bg-success/10 p-5 text-sm font-medium text-success">{{ t('personalCenter.wallet.rechargeSuccess') }}</p>
      <section class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
        <h2 class="text-lg font-semibold">{{ t('rechargeOrder.timeTitle') }}</h2>
        <dl class="mt-4 space-y-3 text-sm">
          <div class="flex flex-wrap justify-between gap-2"><dt class="text-muted-foreground">{{ t('rechargeOrder.createdAtLabel') }}</dt><dd>{{ formatDate(recharge.created_at) }}</dd></div>
          <div v-if="recharge.paid_at" class="flex flex-wrap justify-between gap-2"><dt class="text-muted-foreground">{{ t('rechargeOrder.paidAtLabel') }}</dt><dd>{{ formatDate(recharge.paid_at) }}</dd></div>
          <div v-if="payment?.expires_at" class="flex flex-wrap justify-between gap-2"><dt class="text-muted-foreground">{{ t('payment.expiresAt') }}</dt><dd>{{ formatDate(payment.expires_at) }}</dd></div>
        </dl>
      </section>
      <section v-if="recharge.remark" class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6"><h2 class="text-lg font-semibold">{{ t('rechargeOrder.remarkLabel') }}</h2><p class="mt-3 whitespace-pre-wrap break-words text-sm text-muted-foreground">{{ recharge.remark }}</p></section>
    </div>
  </div>
</template>

<style scoped>
@media (max-width: 1023px) {
  .orbit-recharge-content { padding-bottom: calc(6rem + var(--orbit-bottom-offset, env(safe-area-inset-bottom, 0px))); }
  .orbit-mobile-action {
    position: fixed;
    z-index: 40;
    inset-inline: 0;
    bottom: var(--orbit-bottom-offset, 0px);
    border-top: 1px solid var(--ui-border);
    background: var(--ui-bg-page);
    padding: 12px 16px max(12px, env(safe-area-inset-bottom, 0px));
  }
}
</style>
