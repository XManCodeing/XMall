<script setup lang="ts">
import { onBeforeRouteLeave, useRoute } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { orbitParentPath } from './navigation'
import { useI18n } from 'vue-i18n'
import { Copy, ExternalLink } from 'lucide-vue-next'
import { usePayment } from '@/composables/usePayment'
import { pageAlertVariant, pageAlertToneClass } from '@/utils/alerts'
import PaymentAmountBreakdown from '@/components/payment/PaymentAmountBreakdown.vue'
import PaymentChannelSelector from '@/components/payment/PaymentChannelSelector.vue'
import CheckoutSteps from '@/components/checkout/CheckoutSteps.vue'
import EmptyState from '@/components/EmptyState.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Alert, AlertDescription } from '@/components/ui/alert'

const { t } = useI18n()
const {
  loading, submitting, order, paymentResult, selectedChannelId, copied, walletAddressCopied,
  openedPayWindow, cachedPayment, guestAuth, guestAuthError, walletLoading, useBalance,
  backLink, showGuestAuthForm, walletOnlyPayment, showBalanceOption, configReady, channels,
  selectedChannel, selectedChannelName, cachedChannelName, resultChannelName,
  paymentResultTitle, paymentGuideTitle, paymentGuideTip, showPayLink, showTelegramPayHint, payLinkOpenedTip,
  cryptoWalletAddress, cryptoPaymentDetails, hasCryptoPaymentDetails, qrUsingPayLinkFallback, showQRCode, qrImageUrl,
  orderExpired, orderCanceled, paymentAlert, countdownExpired, countdownText, showCountdown, showResultView, pollingActive, orderItems,
  customerFeeApplied, customerFeeAmountDisplay, payableAmountDisplay, walletBalanceDisplay,
  expectedWalletPaidDisplay, expectedOnlinePayDisplay, expectedOnlinePayCents, requiresOnlineChannel,
  paymentWalletPaidDisplay, paymentOnlinePayDisplay, isChannelDisabledForAmount, channelAmountLimitHint, canSubmitPayment,
  formatDate, statusLabel, formatMoney, hasDiscountAmount, formatDiscountMoney, getLocalizedText, orderItemSkuText, fulfillmentTypeLabelText,
  handleCopyPayLink, handleCopyWalletAddress, handleOpenPayLink, restoreCachedPayment, handleChangePaymentMethod,
  handlePayment, handleGuestAuthSubmit, handleRefresh,
} = usePayment()
const route = useRoute()
const isMobile = useMediaQuery('(max-width: 767px)')
onBeforeRouteLeave(to => {
  if (isMobile.value && to.path === '/checkout') {
    return { path: orbitParentPath('/pay', route.query), replace: true }
  }
})
</script>

<template>
  <div class="orbit-page orbit-page--payment mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:py-12">
    <header class="mb-8 flex flex-wrap items-center justify-between gap-4">
      <div><h1 class="text-3xl font-semibold tracking-tight sm:text-4xl">{{ t('payment.title') }}</h1><p class="mt-2 text-sm text-muted-foreground">{{ t('payment.subtitle') }}</p></div>
      <router-link :to="backLink" class="flex min-h-11 items-center text-sm text-primary">{{ t('payment.backToOrders') }}</router-link>
    </header>
    <CheckoutSteps class="mb-8" current-step="payment" />
    <div v-if="loading" role="status" class="orbit-card space-y-4 rounded-[20px] border bg-card p-6">
      <span class="sr-only">{{ t('common.loading') }}</span>
      <div class="theme-skeleton h-6 w-40 rounded-xl"></div><div class="theme-skeleton h-48 rounded-xl"></div>
    </div>
    <section v-else-if="showGuestAuthForm" class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
      <h2 class="text-lg font-semibold">{{ t('payment.guestAuthTitle') }}</h2>
      <p class="mb-4 mt-2 text-sm text-muted-foreground">{{ t('payment.guestAuthHint') }}</p>
      <form class="space-y-4" @submit.prevent="handleGuestAuthSubmit">
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="space-y-2 text-sm"><span>{{ t('guestOrders.emailPlaceholder') }}</span><Input v-model="guestAuth.email" type="email" autocomplete="email" class="h-11" :placeholder="t('guestOrders.emailPlaceholder')" /></label>
          <label class="space-y-2 text-sm"><span>{{ t('guestOrders.passwordPlaceholder') }}</span><Input v-model="guestAuth.order_password" type="password" autocomplete="current-password" class="h-11" :placeholder="t('guestOrders.passwordPlaceholder')" /></label>
        </div>
        <Alert v-if="guestAuthError" variant="destructive"><AlertDescription>{{ guestAuthError }}</AlertDescription></Alert>
        <Button type="submit" class="min-h-11 rounded-xl">{{ t('payment.guestAuthSubmit') }}</Button>
      </form>
    </section>
    <EmptyState v-else-if="!order" icon="alert" :title="t('payment.orderNotFound')" :action-label="t('payment.backToOrders')" :action-to="backLink" />
    <div v-else class="space-y-6" :class="{ 'orbit-payment-content': !showResultView && !orderExpired && !orderCanceled }">
      <section class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div class="min-w-0">
            <h2 class="text-lg font-semibold">{{ showResultView ? paymentResultTitle : orderCanceled ? t('payment.orderCanceled') : orderExpired ? t('payment.orderExpired') : t('payment.orderInfo') }}</h2>
            <p class="mt-2 break-all text-sm text-muted-foreground">{{ t('payment.orderNo') }}：{{ order.order_no }}</p>
            <p class="mt-1 text-xs text-muted-foreground">{{ t('orderDetail.createdAtLabel') }}：{{ formatDate(order.created_at) }}</p>
            <p v-if="showResultView" class="mt-2 text-sm text-muted-foreground">{{ paymentGuideTip }}</p>
          </div>
          <div class="text-right"><p class="text-xs text-muted-foreground">{{ t('payment.orderStatus') }}</p><p class="mt-1 text-sm font-medium">{{ statusLabel(order.status) }}</p></div>
        </div>
        <div class="mt-5 flex flex-wrap items-center justify-between gap-4 border-t pt-5">
          <div><p class="text-xs text-muted-foreground">{{ t('payment.payableAmountLabel') }}</p><p class="mt-1 text-2xl font-semibold tabular-nums">{{ payableAmountDisplay }}</p></div>
          <Badge v-if="showCountdown" :variant="countdownExpired ? 'danger' : 'success'">{{ t('payment.countdownLabel') }} {{ countdownText }}</Badge>
        </div>
        <p v-if="order.expires_at" class="mt-3 text-xs text-muted-foreground">{{ t('payment.expiresAt') }}：{{ formatDate(order.expires_at) }}</p>
        <p v-if="pollingActive" role="status" class="mt-2 text-xs text-muted-foreground">{{ t('payment.pollingHint') }}</p>
      </section>
      <div v-if="!showResultView && (orderExpired || orderCanceled)" class="flex justify-end"><Button as-child variant="secondary" class="min-h-11 rounded-xl"><router-link :to="backLink">{{ t('payment.backToOrders') }}</router-link></Button></div>
      <div v-else class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_360px]">
        <div class="min-w-0 space-y-6">
          <section v-if="!showResultView" class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
            <h2 class="mb-4 text-lg font-semibold">{{ t('payment.channelTitle') }}</h2>
            <p v-if="!configReady" role="status" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
            <div v-else>
              <div v-if="showBalanceOption" class="mb-4 rounded-xl bg-secondary p-4">
                <div class="flex flex-wrap items-center justify-between gap-3">
                  <div><p class="text-xs text-muted-foreground">{{ t('payment.walletBalanceLabel') }}</p><p class="mt-1 text-sm font-semibold">{{ walletLoading ? t('common.loading') : walletBalanceDisplay }}</p></div>
                  <label class="flex min-h-11 items-center gap-2 text-sm"><input v-model="useBalance" type="checkbox" class="h-5 w-5 accent-primary" :disabled="walletOnlyPayment" /><span>{{ t('payment.useBalance') }}</span></label>
                </div>
                <p v-if="walletOnlyPayment" class="mt-2 text-xs text-warning">{{ t('payment.walletOnlyHint') }}</p>
                <div v-if="useBalance" class="mt-3 space-y-1 text-xs text-muted-foreground"><p>{{ t('payment.walletDeductLabel') }}：{{ expectedWalletPaidDisplay }}</p><p v-if="!walletOnlyPayment">{{ t('payment.onlinePayLabel') }}：{{ expectedOnlinePayDisplay }}</p><p v-if="walletOnlyPayment && expectedOnlinePayCents > 0" class="text-warning">{{ t('payment.walletInsufficientHint') }}</p></div>
              </div>
              <div v-if="cachedPayment" class="mb-4 space-y-2 rounded-xl bg-warning/10 p-4 text-sm text-warning">
                <p class="font-semibold">{{ t('payment.cachedTitle') }}</p><p>{{ t('payment.cachedHint', { channel: cachedChannelName }) }}</p>
                <Button variant="secondary" class="min-h-11 rounded-xl" @click="restoreCachedPayment">{{ t('payment.useCached') }}</Button><p class="text-xs">{{ t('payment.cachedCreateHint') }}</p>
              </div>
              <PaymentChannelSelector v-if="!walletOnlyPayment" :channels="channels" :model-value="selectedChannelId" :show-balance-option="showBalanceOption" :is-channel-disabled-for-amount="isChannelDisabledForAmount" :channel-amount-limit-hint="channelAmountLimitHint" @update:model-value="selectedChannelId = $event" />
            </div>
          </section>
          <section v-if="paymentResult" class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
            <h2 class="text-lg font-semibold">{{ t('payment.infoTitle') }}</h2>
            <p class="mt-2 text-sm text-muted-foreground">{{ t('payment.methodLabel') }}：{{ resultChannelName }}</p>
            <div v-if="showQRCode" class="mt-5 flex flex-col items-center gap-4 text-center">
              <p class="text-sm text-muted-foreground">{{ paymentGuideTitle }}</p>
              <!-- QR codes retain their white quiet zone for scanner contrast in either theme. -->
              <div class="w-full max-w-[260px] rounded-xl bg-white p-2"><img :src="qrImageUrl" :alt="paymentGuideTitle" class="aspect-square w-full object-contain" /></div>
              <p v-if="qrUsingPayLinkFallback" class="text-xs text-muted-foreground">{{ t('payment.qrFallbackHint') }}</p>
            </div>
            <div v-if="hasCryptoPaymentDetails" class="mt-5 space-y-3 rounded-xl bg-secondary p-4">
              <div v-for="item in cryptoPaymentDetails" :key="item.key" class="space-y-1"><p class="text-xs text-muted-foreground">{{ item.label }}</p><p class="break-all text-sm font-medium">{{ item.value }} <span v-if="item.detail" class="font-normal text-muted-foreground">({{ item.detail }})</span></p></div>
              <div v-if="cryptoWalletAddress" class="flex flex-wrap items-center gap-2"><Button variant="outline" class="min-h-11 rounded-xl" @click="handleCopyWalletAddress">{{ t('payment.copyWalletAddress') }}</Button><span v-if="walletAddressCopied" role="status" class="text-xs text-success">{{ t('payment.copied') }}</span></div>
            </div>
            <div v-if="showPayLink || (showResultView && !showQRCode)" class="mt-5 space-y-3">
              <Button type="button" variant="default" class="min-h-11 rounded-xl" @click="handleOpenPayLink"><ExternalLink class="h-4 w-4" />{{ t('payment.openPayLink') }}</Button>
              <p v-if="openedPayWindow" role="status" class="text-xs text-success">{{ payLinkOpenedTip }}</p>
              <p v-if="showTelegramPayHint" class="text-xs text-muted-foreground">{{ t('payment.telegramExternalHint') }}</p>
            </div>
            <div v-if="paymentResult.pay_url" class="mt-3 flex flex-wrap items-center gap-2"><Button type="button" variant="outline" class="min-h-11 rounded-xl" @click="handleCopyPayLink"><Copy class="h-4 w-4" />{{ t('payment.copyPayLink') }}</Button><span v-if="copied" role="status" class="text-xs text-success">{{ t('payment.copied') }}</span></div>
            <p v-if="paymentResult.expires_at" class="mt-4 text-xs text-muted-foreground">{{ t('payment.expiresAt') }}：{{ formatDate(paymentResult.expires_at) }}</p>
          </section>
          <section v-if="orderItems.length" class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
            <h2 class="mb-4 text-lg font-semibold">{{ t('payment.itemsTitle') }}</h2>
            <div class="divide-y"><article v-for="(item, index) in orderItems" :key="index" class="space-y-1 py-3 text-sm first:pt-0 last:pb-0"><p class="break-words font-medium">{{ getLocalizedText(item.title) }}</p><p class="text-xs text-muted-foreground">{{ t('orderDetail.quantityLabel') }}：{{ item.quantity }} · {{ fulfillmentTypeLabelText(item.fulfillment_type) }}</p><p v-if="orderItemSkuText(item)" class="text-xs text-muted-foreground">{{ orderItemSkuText(item) }}</p><p class="tabular-nums">{{ formatMoney(item.total_price, order.currency) }}</p></article></div>
          </section>
        </div>
        <aside class="space-y-4 lg:sticky lg:top-24">
          <PaymentAmountBreakdown class="orbit-amount-breakdown orbit-card" :order="order" :payment-result="paymentResult || {}" :customer-fee-applied="customerFeeApplied" :customer-fee-amount-display="customerFeeAmountDisplay" :payable-amount-display="payableAmountDisplay" :wallet-paid-display="paymentWalletPaidDisplay" :online-pay-display="paymentOnlinePayDisplay" :show-countdown="showCountdown" :countdown-text="countdownText" :polling-active="pollingActive" :format-money="formatMoney" :format-discount-money="formatDiscountMoney" :has-discount-amount="hasDiscountAmount" />
          <section class="orbit-card space-y-4 rounded-[20px] border bg-card p-5">
            <h2 class="text-lg font-semibold">{{ t('payment.actionTitle') }}</h2>
            <Alert v-if="paymentAlert" :variant="pageAlertVariant(paymentAlert.level)" :class="pageAlertToneClass(paymentAlert.level)"><AlertDescription>{{ paymentAlert.message }}</AlertDescription></Alert>
            <template v-if="!showResultView">
              <p v-if="selectedChannel" class="text-sm text-muted-foreground">{{ t('payment.methodLabel') }}：{{ selectedChannelName }}</p>
              <p v-else-if="!requiresOnlineChannel" class="text-sm text-success">{{ t('payment.walletPayOnly') }}</p>
              <p v-else-if="walletOnlyPayment && expectedOnlinePayCents > 0" class="text-sm text-warning">{{ t('payment.walletInsufficientHint') }}</p>
              <p v-else class="text-sm text-warning">{{ t('payment.selectChannelError') }}</p>
              <div class="orbit-mobile-action"><Button class="h-12 w-full rounded-xl font-semibold" :disabled="!canSubmitPayment" @click="handlePayment">{{ submitting ? t('payment.submitting') : t('payment.submitButton') }}</Button></div>
            </template>
            <Button v-else variant="secondary" class="min-h-11 w-full rounded-xl" @click="handleChangePaymentMethod">{{ t('payment.changeMethod') }}</Button>
            <Button variant="outline" class="min-h-11 w-full rounded-xl" :disabled="loading" @click="handleRefresh">{{ t('payment.refreshStatus') }}</Button>
          </section>
        </aside>
      </div>
    </div>
  </div>
</template>

<style scoped>
@media (max-width: 1023px) {
  .orbit-payment-content { padding-bottom: calc(6rem + var(--orbit-bottom-offset, env(safe-area-inset-bottom, 0px))); }
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
