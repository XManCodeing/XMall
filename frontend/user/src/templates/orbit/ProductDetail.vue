<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMediaQuery } from '@vueuse/core'
import { ChevronRight, Package, Minus, Plus, ShoppingCart, AlertCircle } from 'lucide-vue-next'
import { useProductDetail } from '@/composables/useProductDetail'
import { processHtmlForDisplay } from '@/utils/content'
import OrbitSheet from './components/OrbitSheet.vue'

const { t } = useI18n()
const isMobile = useMediaQuery('(max-width: 767px)')
const skuOpen = ref(false)
const {
  getLocalizedText, siteCurrency, formatPrice, getFulfillmentTypeLabel, getPurchaseTypeLabel, getStockStatusLabel,
  hasPromotionPrice, getPromotionPriceAmount, hasSkuPromotionPrice, hasPromotionRules, getPromotionRules,
  formatPromotionRule, formatWholesaleTier, formatRelatedPostDate, normalizeSkuId,
  loading, product, relatedPosts, currentImage, selectedSkuId, quantity, purchaseWarning,
  activeSkus, selectedSku, selectedSkuMemberPrice, hasMemberPrice,
  hasSelectedSkuWholesalePrice, selectedSkuWholesaleFinalPrice, selectedSkuWholesaleRules,
  selectedSkuPromotionFinalPrice, showSelectedSkuMemberBadge,
  isSkuPurchasable, skuDisplayText, skuStockText, quantityEffectiveLimit, quantityEffectiveMin, handleQuantityInput,
  requiresLogin, canPurchase, cannotPurchaseReason, categoryName, images, addToCart, buyNow, goLogin, loadProduct,
} = useProductDetail()
// Presentation only: every amount and eligibility rule comes from the shared composable.
const displayPrice = computed(() => {
  if (selectedSku.value) {
    if (hasSelectedSkuWholesalePrice.value) return selectedSkuWholesaleFinalPrice.value
    if (hasSkuPromotionPrice(selectedSku.value)) return selectedSkuPromotionFinalPrice.value
    if (hasMemberPrice.value) return selectedSkuMemberPrice.value
    return selectedSku.value.price_amount
  }
  return hasPromotionPrice(product.value) ? getPromotionPriceAmount(product.value) : product.value?.price_amount
})
const originalPrice = computed(() => selectedSku.value?.price_amount ?? product.value?.price_amount)
const discounted = computed(() => Number(displayPrice.value) < Number(originalPrice.value))
const chooseSku = (sku: any) => { selectedSkuId.value = normalizeSkuId(sku.id); skuOpen.value = false }
</script>

<template>
  <div class="orbit-detail mx-auto max-w-[1200px] px-5 pb-14 pt-5 sm:px-8 lg:pt-10">
    <nav class="mb-6 flex flex-wrap items-center gap-2 text-xs text-muted-foreground" :aria-label="t('breadcrumb.label')"><RouterLink to="/" class="inline-flex min-h-11 items-center hover:text-primary">{{ t('nav.home') }}</RouterLink><ChevronRight class="h-3 w-3" /><RouterLink to="/products" class="inline-flex min-h-11 items-center hover:text-primary">{{ t('nav.products') }}</RouterLink><template v-if="product"><ChevronRight class="h-3 w-3" /><span class="line-clamp-1 max-w-[220px]">{{ getLocalizedText(product.title) }}</span></template></nav>
    <div v-if="loading" role="status" :aria-label="t('common.loading')" class="grid gap-8 lg:grid-cols-2"><div class="aspect-square rounded-[20px] bg-secondary motion-safe:animate-pulse" /><div class="space-y-5 py-5"><div class="h-9 w-3/4 rounded-xl bg-secondary" /><div class="h-12 w-1/2 rounded-xl bg-secondary" /><div class="h-36 rounded-[20px] bg-secondary" /></div></div>
    <template v-else-if="product">
      <section class="orbit-detail-overview grid items-start gap-8 lg:grid-cols-[1.08fr_1fr] lg:gap-16">
        <div class="orbit-detail-gallery min-w-0 lg:sticky lg:top-28">
          <div class="orbit-detail-hero grid aspect-square place-items-center overflow-hidden rounded-[20px] bg-card p-7 sm:p-10"><img v-if="currentImage" :src="currentImage" :alt="getLocalizedText(product.title)" class="h-full w-full object-contain" /><Package v-else aria-hidden="true" class="h-24 w-24 stroke-1 text-muted-foreground" /></div>
          <div v-if="images.length > 1" class="mt-4 flex flex-wrap justify-center gap-2"><button v-for="(image, index) in images" :key="image" class="h-16 w-16 overflow-hidden rounded-xl border bg-card p-2" :class="image === currentImage ? 'border-primary' : 'border-border'" :aria-label="t('orbit.detail.image', { number: index + 1 })" :aria-pressed="image === currentImage" @click="currentImage = image"><img :src="image" alt="" loading="lazy" class="h-full w-full object-contain" /></button></div>
        </div>
        <div class="orbit-detail-purchase min-w-0 lg:py-5">
          <p v-if="categoryName" class="mb-3 text-xs font-medium text-primary">{{ categoryName }}</p>
          <h1 data-orbit-keep-heading class="break-words text-3xl font-semibold leading-tight tracking-tight lg:text-[2.75rem]">{{ getLocalizedText(product.title) }}</h1>
          <p v-if="getLocalizedText(product.description)" class="mt-4 text-sm leading-relaxed text-muted-foreground">{{ getLocalizedText(product.description) }}</p>
          <div class="orbit-detail-meta mt-5 flex flex-wrap gap-x-4 gap-y-2 text-xs text-muted-foreground"><span v-if="product.stock_status">{{ getStockStatusLabel(product) }}</span><span v-if="product.fulfillment_type !== 'upstream'">{{ getFulfillmentTypeLabel(product.fulfillment_type) }}</span><span>{{ getPurchaseTypeLabel(product.purchase_type) }}</span></div>
          <div class="orbit-detail-pricing my-7 border-b border-border pb-7">
            <p class="mb-2 text-xs text-muted-foreground">{{ t('products.price') }}</p>
            <div class="flex flex-wrap items-baseline gap-3"><span :key="String(displayPrice)" class="orbit-detail-price break-words text-3xl font-semibold tracking-tight tabular-nums">{{ formatPrice(displayPrice, siteCurrency) }}</span><span v-if="discounted" class="text-sm text-muted-foreground line-through">{{ formatPrice(originalPrice, siteCurrency) }}</span></div>
            <div class="mt-3 flex flex-wrap gap-2 text-xs text-primary"><span v-if="showSelectedSkuMemberBadge" class="rounded-full bg-primary/10 px-3 py-1">{{ t('products.memberPriceTag') }}</span><span v-if="hasSelectedSkuWholesalePrice" class="rounded-full bg-primary/10 px-3 py-1">{{ t('products.wholesaleTag') }}</span><span v-if="selectedSku ? hasSkuPromotionPrice(selectedSku) : hasPromotionPrice(product)" class="rounded-full bg-primary/10 px-3 py-1">{{ t('products.promotionTag') }}</span></div>
          </div>
          <div v-if="selectedSkuWholesaleRules.length" class="mb-5 rounded-[20px] bg-card p-4"><h2 class="mb-2 text-sm font-medium">{{ t('products.wholesaleRulesTitle') }}</h2><p v-for="tier in selectedSkuWholesaleRules" :key="`${tier.sku_id}-${tier.min_quantity}`" class="py-1 text-xs text-muted-foreground">{{ formatWholesaleTier(tier) }}</p></div>
          <div v-if="hasPromotionRules(product)" class="mb-5 rounded-[20px] bg-card p-4"><h2 class="mb-2 text-sm font-medium">{{ t('products.promotionRulesTitle') }}</h2><p v-for="rule in getPromotionRules(product)" :key="rule.id" class="py-1 text-xs text-muted-foreground">{{ formatPromotionRule(rule) }}</p></div>
          <div v-if="activeSkus.length" class="mb-6">
            <h2 class="mb-3 text-sm font-semibold">{{ t('productDetail.skuTitle') }}</h2>
            <button v-if="!isMobile || activeSkus.length > 4" type="button" class="flex min-h-14 w-full items-center justify-between gap-3 rounded-xl border border-border bg-card px-4 py-3 text-left lg:hidden" @click="skuOpen = true"><span class="min-w-0"><span class="block break-words text-sm">{{ selectedSku ? skuDisplayText(selectedSku) : t('productDetail.skuRequired') }}</span><span v-if="selectedSku" class="mt-1 block text-xs text-muted-foreground">{{ skuStockText(selectedSku) }}</span></span><ChevronRight class="h-4 w-4 shrink-0 text-muted-foreground" /></button>
            <div v-if="isMobile && activeSkus.length <= 4" class="orbit-mobile-skus">
              <button v-for="sku in activeSkus" :key="sku.id" type="button" :disabled="!isSkuPurchasable(sku)" :aria-pressed="normalizeSkuId(sku.id) === selectedSkuId" @click="chooseSku(sku)">
                <span class="orbit-sku-radio" aria-hidden="true" />
                <span class="min-w-0 flex-1"><span class="block break-words text-sm font-medium">{{ skuDisplayText(sku) }}</span><span class="mt-1 block text-xs text-muted-foreground">{{ skuStockText(sku) }}</span></span>
              </button>
            </div>
            <div class="hidden grid-cols-2 gap-3 lg:grid"><button v-for="sku in activeSkus" :key="sku.id" type="button" class="min-h-16 rounded-xl border px-4 py-3 text-left disabled:cursor-not-allowed disabled:opacity-45" :class="normalizeSkuId(sku.id) === selectedSkuId ? 'border-primary bg-primary/5' : 'border-border bg-card'" :disabled="!isSkuPurchasable(sku)" :aria-pressed="normalizeSkuId(sku.id) === selectedSkuId" @click="chooseSku(sku)"><span class="block break-words text-sm font-medium">{{ skuDisplayText(sku) }}</span><span class="mt-1 block text-xs text-muted-foreground">{{ skuStockText(sku) }}</span></button></div>
          </div>
          <div class="flex items-center justify-between gap-4 py-2"><label for="orbit-quantity" class="text-sm font-semibold">{{ t('productDetail.quantity') }}</label><div class="flex overflow-hidden rounded-xl border border-border bg-card"><button type="button" class="grid h-11 w-11 place-items-center disabled:opacity-35" :aria-label="t('orbit.detail.decrease')" :disabled="quantity <= quantityEffectiveMin" @click="quantity = Math.max(quantityEffectiveMin, quantity - 1)"><Minus class="h-4 w-4" /></button><input id="orbit-quantity" :value="quantity" inputmode="numeric" class="h-11 w-12 border-x border-border bg-card text-center text-sm tabular-nums outline-none focus:bg-secondary" @change="handleQuantityInput" @keydown.enter.prevent="($event.target as HTMLInputElement).blur()" /><button type="button" class="grid h-11 w-11 place-items-center disabled:opacity-35" :aria-label="t('orbit.detail.increase')" :disabled="quantityEffectiveLimit !== null && quantity >= quantityEffectiveLimit" @click="quantity += 1"><Plus class="h-4 w-4" /></button></div></div>
          <p v-if="product.fulfillment_type === 'manual' && product.manual_form_schema?.fields?.length" class="mt-4 rounded-xl bg-secondary p-4 text-xs leading-relaxed text-muted-foreground">{{ t('orbit.detail.manualForm') }}</p>
          <p v-if="cannotPurchaseReason" role="status" class="mt-4 text-sm text-destructive">{{ cannotPurchaseReason }}</p><p v-if="purchaseWarning" role="alert" class="mt-4 text-sm text-warning">{{ purchaseWarning }}</p>
          <div class="mt-7 hidden gap-3 lg:flex"><button v-if="requiresLogin" class="min-h-12 flex-1 rounded-xl bg-primary px-5 text-sm font-medium text-primary-foreground" @click="goLogin">{{ t('productDetail.loginToBuy') }}</button><template v-else><button class="min-h-12 flex-1 rounded-xl bg-primary px-5 text-sm font-medium text-primary-foreground disabled:opacity-40" :disabled="!canPurchase" @click="buyNow">{{ t('productDetail.buyNow') }}</button><button class="min-h-12 rounded-xl border border-border bg-card px-5 text-sm font-medium disabled:opacity-40" :disabled="!canPurchase" @click="addToCart">{{ t('productDetail.addToCart') }}</button></template></div>
        </div>
      </section>
      <section v-if="getLocalizedText(product.description) || getLocalizedText(product.content)" class="mt-14 border-t border-border pt-9 lg:mt-20"><h2 class="mb-6 text-2xl font-semibold tracking-tight">{{ t('productDetail.details') }}</h2><div class="orbit-content prose max-w-none text-foreground dark:prose-invert prose-a:text-primary prose-img:rounded-[20px]"><p v-if="getLocalizedText(product.description)">{{ getLocalizedText(product.description) }}</p><div v-if="getLocalizedText(product.content)" v-html="processHtmlForDisplay(getLocalizedText(product.content))" /></div></section>
      <section v-if="relatedPosts.length" class="mt-12"><h2 class="mb-5 text-2xl font-semibold tracking-tight">{{ t('productDetail.relatedPosts') }}</h2><div class="grid gap-4 lg:grid-cols-3"><RouterLink v-for="post in relatedPosts" :key="post.id" :to="`/blog/${post.slug}`" class="rounded-[20px] bg-card p-6"><p class="text-xs text-muted-foreground">{{ formatRelatedPostDate(post.published_at) }}</p><h3 class="mt-3 text-lg font-medium">{{ getLocalizedText(post.title) }}</h3><p class="mt-3 line-clamp-2 text-sm text-muted-foreground">{{ getLocalizedText(post.summary) }}</p></RouterLink></div></section>
      <div class="orbit-purchase-bar fixed inset-x-0 z-30 border-t border-border bg-card px-5 py-3 lg:hidden"><div class="mx-auto flex max-w-xl items-center gap-3"><div class="min-w-0 flex-1"><p class="truncate text-sm font-semibold tabular-nums">{{ formatPrice(displayPrice, siteCurrency) }}</p><p class="mt-1 text-xs text-muted-foreground">{{ t('productDetail.quantity') }} · {{ quantity }}</p></div><button type="button" class="orbit-add-to-cart grid h-12 w-12 shrink-0 place-items-center rounded-xl bg-secondary text-destructive disabled:opacity-40" :aria-label="t('productDetail.addToCart')" :disabled="!canPurchase" @click="addToCart"><ShoppingCart class="h-7 w-7" /></button><button class="min-h-12 max-w-[55%] shrink-0 rounded-xl bg-primary px-5 py-2 text-sm font-medium text-primary-foreground disabled:opacity-40" :disabled="!requiresLogin && !canPurchase" @click="requiresLogin ? goLogin() : buyNow()">{{ requiresLogin ? t('productDetail.loginToBuy') : t('productDetail.buyNow') }}</button></div></div>
      <OrbitSheet v-model:open="skuOpen" :title="t('productDetail.skuTitle')"><div class="space-y-2"><button v-for="sku in activeSkus" :key="sku.id" class="min-h-16 w-full rounded-xl border px-4 py-3 text-left disabled:opacity-40" :class="normalizeSkuId(sku.id) === selectedSkuId ? 'border-primary bg-primary/5' : 'border-border bg-card'" :disabled="!isSkuPurchasable(sku)" :aria-pressed="normalizeSkuId(sku.id) === selectedSkuId" @click="chooseSku(sku)"><span class="block text-sm font-medium">{{ skuDisplayText(sku) }}</span><span class="mt-1 block text-xs text-muted-foreground">{{ skuStockText(sku) }}</span></button></div></OrbitSheet>
    </template>
    <div v-else class="rounded-[20px] bg-card px-6 py-16 text-center"><AlertCircle class="mx-auto mb-4 h-10 w-10 stroke-1 text-muted-foreground" /><p class="text-muted-foreground">{{ t('productDetail.notFound') }}</p><button class="mt-5 min-h-11 rounded-xl bg-primary px-5 text-sm text-primary-foreground" @click="loadProduct">{{ t('errorBoundary.retry') }}</button></div>
  </div>
</template>

<style scoped>
.orbit-content { overflow-wrap: anywhere; }
.orbit-detail-gallery img, .orbit-content :deep(img) { filter: brightness(var(--orbit-detail-image-brightness, 1)); }
@media (max-width: 767px) {
  .orbit-detail { padding: 16px 16px 160px; }
  .orbit-detail > nav { display: none; }
  .orbit-detail-overview { gap: 16px; }
  .orbit-detail-hero { height: clamp(180px, 52vw, 240px); aspect-ratio: auto; padding: 16px; border-radius: 24px; background: var(--ui-bg-soft); }
  .orbit-detail-hero img { border-radius: 14px; }
  .orbit-detail-purchase { padding: 24px 20px; border: 1px solid var(--ui-border); border-radius: 24px; background: var(--ui-bg-elevated); box-shadow: var(--ui-shadow-1); }
  .orbit-detail-purchase h1 { font-size: 26px; line-height: 1.2; letter-spacing: -.04em; text-wrap: wrap; }
  .orbit-detail-purchase > p:first-child { margin-bottom: 8px; font-size: 11px; }
  .orbit-detail-purchase h1 + p { margin-top: 10px; font-size: 13px; }
  .orbit-detail-meta { margin-top: 16px; gap: 6px 12px; font-size: 11px; }
  .orbit-detail-pricing { margin: 20px 0; padding-bottom: 20px; }
  .orbit-detail-price { font-size: 28px; }
  .orbit-detail-pricing > div:last-child:empty { display: none; }
  .orbit-detail-purchase h2, .orbit-detail-purchase label { font-size: 12px; font-weight: 600; }
  .orbit-mobile-skus { display: grid; gap: 9px; }
  .orbit-mobile-skus > button { display: flex; align-items: center; gap: 12px; width: 100%; min-height: 72px; padding: 14px; border: 1px solid var(--ui-border); border-radius: 14px; text-align: left; background: var(--ui-bg-elevated); }
  .orbit-mobile-skus > button[aria-pressed='true'] { border-color: var(--ui-accent); background: var(--ui-accent-soft); box-shadow: inset 0 0 0 .5px var(--ui-accent); }
  .orbit-mobile-skus > button:disabled { opacity: .45; }
  .orbit-sku-radio { flex-shrink: 0; width: 18px; height: 18px; border: 1px solid var(--ui-border-strong); border-radius: 50%; }
  [aria-pressed='true'] > .orbit-sku-radio { border: 5px solid var(--ui-accent); background: var(--ui-bg-elevated); }
  .orbit-detail > section:not(.orbit-detail-overview) { margin-top: 28px; padding-top: 24px; }
  .orbit-detail > section > h2 { font-size: 20px; margin-bottom: 16px; }
  .orbit-purchase-bar { padding: 12px 16px; background: var(--ui-bg-overlay-strong); backdrop-filter: blur(20px); }
  .orbit-purchase-bar > div { gap: 10px; }
  .orbit-purchase-bar button:last-child { min-width: 120px; border-radius: 14px; }
}
@media (min-width: 1024px) {
  .orbit-detail-purchase { padding: 32px; border: 1px solid var(--ui-border); border-radius: 28px; background: var(--ui-bg-elevated); box-shadow: var(--ui-shadow-1); }
  .orbit-detail-purchase h1 { font-size: 34px; letter-spacing: -.04em; }
  .orbit-detail-purchase button[aria-pressed] { border-radius: 15px; }
  .orbit-detail-purchase button[aria-pressed='true'] { background: var(--ui-accent-soft); box-shadow: inset 0 0 0 .5px var(--ui-accent); }
}
@media (min-width: 768px) and (prefers-reduced-motion: no-preference) {
  .orbit-detail-price { display: inline-block; animation: orbit-price-in 400ms var(--orbit-ease); }
  @keyframes orbit-price-in { from { opacity: .4; transform: translateY(4px); } to { opacity: 1; transform: none; } }
}
@media (max-width: 767px) and (prefers-reduced-motion: no-preference) {
  .orbit-detail-price { display: inline-block; animation: orbit-mobile-price-in 240ms var(--orbit-ease); }
  @keyframes orbit-mobile-price-in { from { opacity: .5; transform: translateY(3px); } to { opacity: 1; transform: none; } }
}
.orbit-content :deep(table) { display: block; max-width: 100%; overflow-x: auto; }
.orbit-content :deep(img) { max-width: 100%; height: auto; }
.orbit-purchase-bar { bottom: var(--orbit-mobile-nav-height, 0px); padding-bottom: max(.75rem, env(safe-area-inset-bottom)); }
@media (max-width: 1023px) { .orbit-detail { padding-bottom: calc(7rem + var(--orbit-mobile-nav-height, 0px) + env(safe-area-inset-bottom)); } }
</style>
