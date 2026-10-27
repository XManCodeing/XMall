<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Minus, Plus, Trash2 } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import EmptyState from '@/components/EmptyState.vue'
import SmartImage from '@/components/SmartImage.vue'
import CheckoutSteps from '@/components/checkout/CheckoutSteps.vue'
import { useCart } from '@/composables/useCart'

const { t } = useI18n()
const {
  getLocalizedText, formatPrice, totalCurrency, cartItems, totalItems, totalAmount,
  cartItemKey, cartItemImage, itemSkuDisplay, itemSubtotal, itemStockHint, quantityWarning,
  itemPurchaseMin, itemMaxQuantity, removeWithUndo, updateQty, handleQtyChange,
} = useCart()
</script>

<template>
  <div class="orbit-page orbit-page--cart mx-auto max-w-6xl px-4 py-8 sm:px-6 lg:py-12">
    <header class="mb-8">
      <h1 class="text-3xl font-semibold tracking-tight sm:text-4xl">{{ t('cart.title') }}</h1>
      <p class="mt-2 text-sm text-muted-foreground">{{ t('cart.subtitle') }}</p>
    </header>
    <CheckoutSteps class="mb-8" current-step="cart" />
    <EmptyState v-if="cartItems.length === 0" icon="cart" :title="t('cart.empty')" :action-label="t('cart.emptyAction')" action-to="/products" />
    <div v-else class="orbit-cart-content grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <section class="min-w-0 space-y-4" :aria-label="t('checkout.itemsTitle')">
        <article v-for="item in cartItems" :key="cartItemKey(item)" class="orbit-card rounded-[20px] border bg-card p-4 sm:p-6">
          <div class="flex items-start gap-4">
            <router-link :to="`/products/${item.slug}`" class="h-20 w-20 shrink-0 overflow-hidden rounded-xl bg-muted sm:h-24 sm:w-24">
              <SmartImage :src="cartItemImage(item)" :alt="getLocalizedText(item.title)" img-class="h-20 w-20 object-cover sm:h-24 sm:w-24" />
            </router-link>
            <div class="min-w-0 flex-1">
              <router-link :to="`/products/${item.slug}`" class="break-words text-base font-semibold hover:text-primary">{{ getLocalizedText(item.title) }}</router-link>
              <p v-if="itemSkuDisplay(item)" class="mt-1 break-words text-xs text-muted-foreground">{{ itemSkuDisplay(item) }}</p>
              <p class="mt-2 text-sm tabular-nums">{{ formatPrice(item.priceAmount, totalCurrency) }}</p>
              <div class="mt-2 flex flex-wrap gap-2">
                <Badge :variant="item.purchaseType === 'guest' ? 'warning' : 'success'" size="xs">{{ item.purchaseType === 'guest' ? t('productPurchase.guest') : t('productPurchase.member') }}</Badge>
                <Badge :variant="item.fulfillmentType === 'auto' ? 'info' : 'neutral'" size="xs">{{ item.fulfillmentType === 'auto' ? t('products.fulfillmentType.auto') : t('products.fulfillmentType.manual') }}</Badge>
              </div>
            </div>
          </div>
          <p v-if="itemStockHint(item)" class="mt-3 text-xs text-muted-foreground">{{ itemStockHint(item) }}</p>
          <div class="mt-4 flex flex-wrap items-center justify-between gap-3 border-t pt-4">
            <div class="flex items-center gap-1">
              <Button variant="ghost" size="icon" class="h-11 w-11 rounded-xl" :disabled="item.quantity <= itemPurchaseMin(item)" :aria-label="`${t('checkout.quantityLabel')} −`" @click="updateQty(item, item.quantity - 1)"><Minus class="h-4 w-4" /></Button>
              <input :id="`orbit-cart-qty-${cartItemKey(item)}`" :name="`orbit-cart-qty-${cartItemKey(item)}`" type="number" :value="item.quantity" :min="itemPurchaseMin(item)" :max="itemMaxQuantity(item)" :aria-label="t('checkout.quantityLabel')" class="orbit-quantity h-11 w-12 rounded-xl border bg-background text-center text-base tabular-nums" @change="handleQtyChange(item, $event)" />
              <Button variant="ghost" size="icon" class="h-11 w-11 rounded-xl" :disabled="item.quantity >= itemMaxQuantity(item)" :aria-label="`${t('checkout.quantityLabel')} +`" @click="updateQty(item, item.quantity + 1)"><Plus class="h-4 w-4" /></Button>
              <Button variant="ghost" size="icon" class="h-11 w-11 rounded-xl text-muted-foreground" :aria-label="t('cart.remove')" @click="removeWithUndo(item)"><Trash2 class="h-4 w-4" /></Button>
            </div>
            <p class="text-base font-semibold tabular-nums">{{ itemSubtotal(item) }}</p>
          </div>
          <p v-if="quantityWarning(item)" role="status" class="mt-3 rounded-xl bg-warning/10 p-3 text-xs text-warning">{{ quantityWarning(item) }}</p>
        </article>
      </section>
      <aside class="orbit-card rounded-[20px] border bg-card p-5 lg:sticky lg:top-24">
        <h2 class="text-lg font-semibold">{{ t('cart.summaryTitle') }}</h2>
        <dl class="my-5 space-y-3 text-sm">
          <div class="flex justify-between gap-4"><dt class="text-muted-foreground">{{ t('cart.itemsCount') }}</dt><dd class="tabular-nums">{{ totalItems }}</dd></div>
          <div class="flex justify-between gap-4 border-t pt-4"><dt>{{ t('cart.totalLabel') }}</dt><dd class="font-semibold tabular-nums">{{ formatPrice(totalAmount, totalCurrency) }}</dd></div>
        </dl>
        <p class="mb-5 text-xs leading-relaxed text-muted-foreground">{{ t('cart.disclaimer') }}</p>
        <div class="orbit-mobile-action">
          <Button as-child size="lg" class="h-12 w-full rounded-xl font-semibold"><router-link to="/checkout">{{ t('cart.checkout') }}</router-link></Button>
        </div>
        <router-link to="/products" class="mt-3 flex min-h-11 items-center justify-center text-sm text-primary">{{ t('cart.emptyAction') }}</router-link>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.orbit-quantity { appearance: textfield; }
.orbit-quantity::-webkit-inner-spin-button,
.orbit-quantity::-webkit-outer-spin-button { appearance: none; margin: 0; }
@media (max-width: 1023px) {
  .orbit-cart-content { padding-bottom: calc(6rem + var(--orbit-bottom-offset, env(safe-area-inset-bottom, 0px))); }
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
