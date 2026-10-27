<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { useMediaQuery } from '@vueuse/core'
import { useConfirmDialog } from '@/composables/useConfirmDialog'
import { useI18n } from 'vue-i18n'
import { useCheckout } from '@/composables/useCheckout'
import { pageAlertVariant, pageAlertToneClass } from '@/utils/alerts'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Alert, AlertDescription } from '@/components/ui/alert'
import EmptyState from '@/components/EmptyState.vue'
import SmartImage from '@/components/SmartImage.vue'
import CheckoutSteps from '@/components/checkout/CheckoutSteps.vue'
import CheckoutManualForm from '@/components/checkout/CheckoutManualForm.vue'
import ImageCaptcha from '@/components/captcha/ImageCaptcha.vue'
import TurnstileCaptcha from '@/components/captcha/TurnstileCaptcha.vue'

const { t } = useI18n()
const {
  userAuthStore, getLocalizedText, formatPrice, getImageUrl,
  isBuyNowMode, cartItems, totalItems, cartItemKey, checkoutItemImage, itemSkuDisplay,
  itemStockExceeded, itemStockHint, checkoutItemCurrency, checkoutItemPriceParts,
  checkoutItemOriginalPriceParts, checkoutItemHasPriceDiscount,
  manualFormProducts, manualFormData, submitAttempted, getManualFieldLabel, getManualFieldPlaceholder, manualFieldError,
  couponCode, isResellerTenant, checkoutMode, guestEmail, guestPassword, guestEmailValid,
  guestCaptchaEnabled, captchaProvider, guestCaptchaPayload, guestTurnstileToken, guestTurnstileSiteKey,
  guestImageCaptchaRef, guestTurnstileRef, handleGuestCaptchaConfigStale,
  previewCurrency, previewOriginal, previewCoupon, previewPromotion, previewWholesale, previewMemberDiscount, previewTotal,
  previewLoading, couponRefreshing, previewStatusText, hasPositiveAmount, formatDiscountPrice, checkoutAlert,
  showBalanceOption, walletLoading, walletBalance, useBalance, walletOnlyPayment,
  expectedWalletPaidDisplay, expectedOnlinePayDisplay, expectedOnlinePayCents,
  requiresOnlineChannel, paymentChannels, selectedChannelId, isChannelDisabledForAmount, channelAmountLimitHint,
  handleSelectChannel, formatChannelFeeRate, formatChannelFixedFee, submitting, canSubmit, handleSubmit,
} = useCheckout()

const isMobile = useMediaQuery('(max-width: 767px)')
const edited = ref(false)
const { confirm } = useConfirmDialog()
let leavePrompt: Promise<boolean> | undefined
const protectForm = () => {
  if (!isMobile.value) return true
  // A completed submission clears the source items before navigating to payment.
  if (!cartItems.value.length) return true
  if (submitting.value) return false
  if (!edited.value) return true
  if (!leavePrompt) {
    leavePrompt = confirm({
      title: t('orbit.actions.leaveTitle'), message: t('orbit.actions.leaveMessage'),
      confirmText: t('orbit.actions.leave'), cancelText: t('orbit.actions.stay'),
    }).finally(() => { leavePrompt = undefined })
  }
  return leavePrompt
}
onBeforeRouteLeave(protectForm)
onBeforeRouteUpdate((to, from) => to.query.mode === from.query.mode ? true : protectForm())
const beforeUnload = (event: BeforeUnloadEvent) => {
  if (isMobile.value && cartItems.value.length && (edited.value || submitting.value)) {
    event.preventDefault()
    event.returnValue = ''
  }
}
window.addEventListener('beforeunload', beforeUnload)
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

void guestImageCaptchaRef
void guestTurnstileRef
</script>

<template>
  <div @input="edited = true" @change="edited = true" class="orbit-page orbit-page--checkout mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:py-12">
    <header class="mb-8">
      <h1 class="text-3xl font-semibold tracking-tight sm:text-4xl">{{ t('checkout.title') }}</h1>
      <p class="mt-2 text-sm text-muted-foreground">{{ t('checkout.subtitle') }}</p>
    </header>
    <CheckoutSteps class="mb-8" current-step="checkout" :step-keys="isBuyNowMode ? ['checkout', 'payment'] : ['cart', 'checkout', 'payment']" />
    <EmptyState v-if="cartItems.length === 0" icon="cart" :title="t('checkout.empty')" :action-label="t('checkout.emptyAction')" action-to="/products" />
    <div v-else class="orbit-checkout-content grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_360px]">
      <div class="min-w-0 space-y-6">
        <section class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
          <h2 class="mb-5 text-lg font-semibold">{{ t('checkout.itemsTitle') }}</h2>
          <div class="divide-y">
            <article v-for="item in cartItems" :key="cartItemKey(item)" class="flex gap-4 py-4 first:pt-0 last:pb-0">
              <div class="h-16 w-16 shrink-0 overflow-hidden rounded-xl bg-muted sm:h-20 sm:w-20">
                <SmartImage :src="checkoutItemImage(item)" :alt="getLocalizedText(item.title)" img-class="h-full w-full object-cover" />
              </div>
              <div class="min-w-0 flex-1">
                <router-link :to="`/products/${item.slug}`" class="break-words text-sm font-semibold hover:text-primary">{{ getLocalizedText(item.title) }}</router-link>
                <p v-if="itemSkuDisplay(item)" class="mt-1 break-words text-xs text-muted-foreground">{{ itemSkuDisplay(item) }}</p>
                <p class="mt-1 text-xs text-muted-foreground">{{ t('checkout.quantityLabel') }}：{{ item.quantity }}</p>
                <p v-if="itemStockHint(item)" class="mt-2 text-xs" :class="itemStockExceeded(item) ? 'text-warning' : 'text-muted-foreground'">{{ itemStockHint(item) }}</p>
                <div class="mt-2 flex flex-wrap items-baseline gap-2 text-sm tabular-nums">
                  <span class="font-semibold">{{ checkoutItemPriceParts(item).integer }}{{ checkoutItemPriceParts(item).decimal }} {{ checkoutItemCurrency }}</span>
                  <template v-if="checkoutItemHasPriceDiscount(item)">
                    <span class="text-xs text-muted-foreground line-through">{{ checkoutItemOriginalPriceParts(item).integer }}{{ checkoutItemOriginalPriceParts(item).decimal }} {{ checkoutItemCurrency }}</span>
                    <span class="text-xs text-success">{{ t('checkout.discountedPriceLabel') }}</span>
                  </template>
                </div>
              </div>
            </article>
          </div>
        </section>
        <CheckoutManualForm class="orbit-manual-form orbit-card" :manual-form-products="manualFormProducts" v-model="manualFormData" :submit-attempted="submitAttempted" :get-manual-field-label="getManualFieldLabel" :get-manual-field-placeholder="getManualFieldPlaceholder" :manual-field-error="manualFieldError" />
        <section v-if="!userAuthStore.isAuthenticated" class="orbit-card space-y-4 rounded-[20px] border bg-card p-5 sm:p-6">
          <h2 class="text-lg font-semibold">{{ t('checkout.modeTitle') }}</h2>
          <div class="flex flex-wrap gap-3">
            <Button class="min-h-11 rounded-xl" :variant="checkoutMode === 'guest' ? 'default' : 'secondary'" @click="checkoutMode = 'guest'">{{ t('checkout.guestPurchase') }}</Button>
            <Button as-child variant="secondary" class="min-h-11 rounded-xl"><router-link to="/auth/login">{{ t('checkout.memberPurchase') }}</router-link></Button>
          </div>
          <template v-if="checkoutMode === 'guest'">
            <div class="grid gap-4 sm:grid-cols-2">
              <label class="space-y-2 text-sm"><span>{{ t('guestOrders.emailPlaceholder') }}</span><Input v-model="guestEmail" type="email" autocomplete="email" class="h-11" :placeholder="t('checkout.guestEmailPlaceholder')" /></label>
              <label class="space-y-2 text-sm"><span>{{ t('guestOrders.passwordPlaceholder') }}</span><Input v-model="guestPassword" type="password" autocomplete="new-password" class="h-11" :placeholder="t('checkout.guestPasswordPlaceholder')" /></label>
            </div>
            <p v-if="guestEmail && !guestEmailValid" role="alert" class="text-xs text-destructive">{{ t('error.email_invalid') }}</p>
            <div v-if="guestCaptchaEnabled" class="space-y-2">
              <p class="text-xs text-muted-foreground">{{ t('auth.common.captchaLabel') }}</p>
              <ImageCaptcha v-if="captchaProvider === 'image'" ref="guestImageCaptchaRef" v-model="guestCaptchaPayload" :disabled="submitting" @config-stale="handleGuestCaptchaConfigStale" />
              <TurnstileCaptcha v-else-if="captchaProvider === 'turnstile'" ref="guestTurnstileRef" v-model="guestTurnstileToken" :site-key="guestTurnstileSiteKey" />
            </div>
            <div class="rounded-xl bg-secondary p-4 text-xs leading-relaxed text-muted-foreground">
              <p class="font-semibold text-foreground">{{ t('checkout.guestInstructions.title') }}</p>
              <ul class="mt-2 list-disc space-y-1 pl-4"><li>{{ t('checkout.guestInstructions.email') }}</li><li>{{ t('checkout.guestInstructions.password') }}</li></ul>
            </div>
          </template>
        </section>
        <section v-if="!isResellerTenant" class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
          <label for="orbit-coupon" class="mb-3 block text-lg font-semibold">{{ t('checkout.couponTitle') }}</label>
          <Input id="orbit-coupon" v-model="couponCode" class="h-11" :placeholder="t('checkout.couponPlaceholder')" />
        </section>
        <section class="orbit-card rounded-[20px] border bg-card p-5 sm:p-6">
          <h2 class="mb-4 text-lg font-semibold">{{ t('checkout.paymentMethod') }}</h2>
          <div v-if="showBalanceOption" class="mb-4 rounded-xl bg-secondary p-4">
            <div class="flex flex-wrap items-center justify-between gap-3">
              <div><p class="text-xs text-muted-foreground">{{ t('payment.walletBalanceLabel') }}</p><p class="mt-1 text-sm font-semibold tabular-nums">{{ walletLoading ? t('common.loading') : formatPrice(walletBalance, previewCurrency) }}</p></div>
              <label class="flex min-h-11 items-center gap-2 text-sm"><input v-model="useBalance" type="checkbox" class="h-5 w-5 accent-primary" :disabled="walletOnlyPayment" /><span>{{ t('payment.useBalance') }}</span></label>
            </div>
            <p v-if="walletOnlyPayment" class="mt-2 text-xs text-warning">{{ t('payment.walletOnlyHint') }}</p>
            <div v-if="useBalance" class="mt-3 space-y-1 text-xs text-muted-foreground">
              <p>{{ t('payment.walletDeductLabel') }}：{{ expectedWalletPaidDisplay }}</p>
              <p v-if="!walletOnlyPayment">{{ t('payment.onlinePayLabel') }}：{{ expectedOnlinePayDisplay }}</p>
              <p v-if="walletOnlyPayment && expectedOnlinePayCents > 0" class="text-warning">{{ t('payment.walletInsufficientHint') }}</p>
            </div>
          </div>
          <template v-if="!walletOnlyPayment">
            <div v-if="requiresOnlineChannel && paymentChannels.length" class="grid gap-2 sm:grid-cols-2">
              <button v-for="channel in paymentChannels" :key="channel.id" type="button" :disabled="isChannelDisabledForAmount(channel)" :aria-pressed="selectedChannelId === channel.id" class="min-h-14 rounded-xl border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60" :class="selectedChannelId === channel.id && !isChannelDisabledForAmount(channel) ? 'border-primary bg-primary/10' : 'bg-card hover:bg-secondary'" @click="handleSelectChannel(channel)">
                <span class="flex items-center gap-3"><img v-if="channel.icon" :src="getImageUrl(channel.icon)" alt="" loading="lazy" class="h-6 w-6 shrink-0 object-contain" /><span class="break-words text-sm font-medium">{{ channel.name }}</span></span>
                <span v-if="channel.fee_policy === 'customer_surcharge'" class="mt-2 block text-xs text-warning">{{ t('payment.feeLabel') }}：{{ formatChannelFeeRate(channel) }} · {{ t('payment.fixedFeeLabel') }}：{{ formatChannelFixedFee(channel) }}</span>
                <span v-if="isChannelDisabledForAmount(channel)" class="mt-2 block text-xs text-warning">{{ channelAmountLimitHint(channel) }}</span>
              </button>
            </div>
            <p v-else-if="requiresOnlineChannel" class="text-sm text-muted-foreground">{{ t('checkout.noPaymentChannels') }}</p>
          </template>
          <p v-if="!requiresOnlineChannel" class="text-sm text-success">{{ t('checkout.walletCoversAll') }}</p>
        </section>
      </div>
      <aside class="orbit-card rounded-[20px] border bg-card p-5 lg:sticky lg:top-24">
        <h2 class="text-lg font-semibold">{{ t('checkout.submitTitle') }}</h2>
        <dl class="my-5 space-y-3 text-sm">
          <div class="flex justify-between gap-3"><dt class="text-muted-foreground">{{ t('cart.itemsCount') }}</dt><dd>{{ totalItems }}</dd></div>
          <div class="flex justify-between gap-3"><dt class="text-muted-foreground">{{ t('checkout.previewOriginal') }}</dt><dd class="tabular-nums">{{ formatPrice(previewOriginal, previewCurrency) }}</dd></div>
          <template v-if="!isResellerTenant">
            <div v-for="discount in [{ key: 'previewCoupon', value: previewCoupon }, { key: 'previewPromotion', value: previewPromotion }, { key: 'previewWholesale', value: previewWholesale }]" :key="discount.key" class="flex justify-between gap-3"><dt class="text-muted-foreground">{{ t(`checkout.${discount.key}`) }}</dt><dd class="tabular-nums" :class="hasPositiveAmount(discount.value) ? 'text-success' : ''">{{ formatDiscountPrice(discount.value, previewCurrency) }}</dd></div>
          </template>
          <div v-if="Number(previewMemberDiscount) > 0" class="flex justify-between gap-3"><dt class="text-muted-foreground">{{ t('checkout.previewMemberDiscount') }}</dt><dd class="tabular-nums text-success">−{{ formatPrice(previewMemberDiscount, previewCurrency) }}</dd></div>
          <div class="flex justify-between gap-3 border-t pt-4 text-base font-semibold"><dt>{{ t('checkout.previewTotal') }}</dt><dd class="tabular-nums">{{ formatPrice(previewTotal, previewCurrency) }}</dd></div>
        </dl>
        <p class="mb-4 text-xs leading-relaxed text-muted-foreground">{{ t('checkout.submitHint') }}</p>
        <p v-if="previewLoading || couponRefreshing" role="status" class="mb-3 text-xs text-muted-foreground">{{ previewStatusText }}</p>
        <Alert v-if="checkoutAlert" :variant="pageAlertVariant(checkoutAlert.level)" :class="['mb-4', pageAlertToneClass(checkoutAlert.level)]"><AlertDescription>{{ checkoutAlert.message }}</AlertDescription></Alert>
        <div class="orbit-mobile-action"><Button size="lg" class="h-12 w-full rounded-xl font-semibold" :disabled="!canSubmit" @click="handleSubmit">{{ submitting ? t('checkout.submitting') : t('checkout.submitButton') }}</Button></div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
@media (max-width: 1023px) {
  .orbit-checkout-content { padding-bottom: calc(6rem + var(--orbit-bottom-offset, env(safe-area-inset-bottom, 0px))); }
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
