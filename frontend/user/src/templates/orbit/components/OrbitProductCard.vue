<script setup lang="ts">
import { computed } from 'vue'
import { ArrowUpRight, Package } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import { useLocalized, useProductLabels } from '@/composables/useProduct'
import { getImageUrl } from '@/utils/image'
const props = defineProps<{ product: any }>()
const { t } = useI18n()
const { getLocalizedText, formatPrice } = useLocalized()
const { isSoldOut, hasPromotionPrice, getPromotionPriceAmount, getStockStatusLabel } = useProductLabels()
const image = computed(() => {
  const images = props.product.images
  return getImageUrl((Array.isArray(images) ? images[0] : images?.images?.[0]) || '')
})
const soldOut = computed(() => isSoldOut(props.product))
</script>

<template>
  <RouterLink :to="`/products/${product.slug}`" class="orbit-product group flex h-full flex-col overflow-hidden rounded-[20px] bg-card text-foreground focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-primary">
    <div class="orbit-product-media relative grid aspect-square place-items-center overflow-hidden">
      <img v-if="image" :src="image" :alt="getLocalizedText(product.title)" loading="lazy" class="absolute inset-0 h-full w-full object-cover transition-transform duration-500 motion-safe:group-hover:scale-105" />
      <Package v-else aria-hidden="true" class="h-16 w-16 stroke-1 text-muted-foreground" />
      <span v-if="soldOut" class="absolute left-4 top-4 rounded-full bg-card px-3 py-1 text-xs text-muted-foreground">{{ t('products.stockStatus.outOfStock') }}</span>
      <span v-else-if="hasPromotionPrice(product)" class="absolute left-4 top-4 rounded-full bg-card px-3 py-1 text-xs font-medium text-primary">{{ t('products.promotionTag') }}</span>
    </div>
    <div class="orbit-product-info flex flex-1 flex-col p-5 lg:p-6">
      <span v-if="product.category?.name" class="mb-2 text-xs font-medium text-muted-foreground">{{ getLocalizedText(product.category.name) }}</span>
      <h3 class="line-clamp-2 break-words text-xl font-semibold leading-snug tracking-tight">{{ getLocalizedText(product.title) }}</h3>
      <p v-if="getLocalizedText(product.description)" class="orbit-product-description mt-2 line-clamp-2 text-sm leading-relaxed text-muted-foreground">{{ getLocalizedText(product.description) }}</p>
      <div class="orbit-product-price mt-auto flex items-end justify-between gap-3 pt-6">
        <div class="min-w-0">
          <p class="break-words text-base font-semibold tabular-nums">{{ formatPrice(hasPromotionPrice(product) ? getPromotionPriceAmount(product) : product.price_amount) }}</p>
          <p v-if="hasPromotionPrice(product)" class="mt-0.5 text-xs text-muted-foreground line-through">{{ formatPrice(product.price_amount) }}</p>
          <p v-if="product.stock_status && !soldOut" class="mt-1 text-xs text-muted-foreground">{{ getStockStatusLabel(product) }}</p>
        </div>
        <span class="grid h-11 w-11 shrink-0 place-items-center rounded-xl bg-secondary text-primary" :aria-label="t('common.viewDetails')"><ArrowUpRight class="h-5 w-5" /></span>
      </div>
    </div>
  </RouterLink>
</template>

<style scoped>
.orbit-product { transition: box-shadow 200ms ease; box-shadow: var(--ui-shadow-1); }
.orbit-product-media { flex-shrink: 0; background: inherit; }
.orbit-product-media img { filter: brightness(var(--orbit-image-brightness, 1)); }
@media (hover: hover) { .orbit-product:hover { box-shadow: var(--ui-shadow-card); } }
@media (max-width: 767px) {
  .orbit-product { min-width: 0; border-radius: 20px; transition: transform 220ms var(--orbit-ease), box-shadow 220ms ease; }
  .orbit-product:active { transform: scale(.975); }
  .orbit-product-media > svg { width: 44px; height: 44px; }
  .orbit-product-media > span { top: 8px; left: 8px; padding: 4px 8px; font-size: 10px; }
  .orbit-product-info { padding: 12px; }
  .orbit-product-info > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 10px; margin-bottom: 5px; }
  .orbit-product-info h3 { font-size: 14px; line-height: 1.45; min-height: 2.9em; font-weight: 600; }
  .orbit-product-description { display: none; }
  .orbit-product-price { padding-top: 12px; gap: 4px; }
  .orbit-product-price > span { display: none; }
  .orbit-product-price p:first-child { font-size: 15px; letter-spacing: -.03em; }
  .orbit-product-price p:not(:first-child) { font-size: 10px; }
}
@media (min-width: 768px) {
  .orbit-product { border-radius: 24px; box-shadow: var(--ui-shadow-1); transition: box-shadow 300ms var(--orbit-ease), transform 400ms var(--orbit-ease); }
  .orbit-product-info h3 { font-size: 18px; font-weight: 600; letter-spacing: -.025em; }
  .orbit-product-price > span { background: var(--ui-bg-soft); color: var(--ui-text-secondary); border-radius: 50%; transition: background-color 200ms ease, transform 300ms var(--orbit-ease); }
  .orbit-product:active { transform: scale(.992); }
}
@media (min-width: 768px) and (hover: hover) and (prefers-reduced-motion: no-preference) {
  .orbit-product:hover { transform: translateY(-3px); }
  .orbit-product:hover .orbit-product-price > span { transform: translate(2px, -2px); }
}
@media (prefers-reduced-motion: reduce) { .orbit-product, .orbit-product:active { transition: none; transform: none; } }
</style>
