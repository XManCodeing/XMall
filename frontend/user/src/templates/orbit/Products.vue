<script lang="ts">
// One short-lived, in-memory browsing snapshot; never stores credentials or checkout data.
let mobileBrowseSnapshot: {
  path: string; savedAt: number; scrollY: number; selectedCategory: number | null;
  searchQuery: string; currentPage: number; products: any[]; mobileProducts: any[]; totalPages: number;
} | undefined
</script>

<script setup lang="ts">
import { computed, inject, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRoute } from 'vue-router'
import { useIntersectionObserver, useMediaQuery } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { ChevronLeft, ChevronRight, Search, SlidersHorizontal, X, PackageOpen, RotateCw } from 'lucide-vue-next'
import { useProductList } from '@/composables/useProductList'
import { useLocalized } from '@/composables/useProduct'
import { usePageSeo } from '@/composables/usePageSeo'
import OrbitProductCard from './components/OrbitProductCard.vue'
import OrbitSheet from './components/OrbitSheet.vue'

const { t } = useI18n()
const route = useRoute()
const searchInput = ref<HTMLInputElement>()
const searchRequested = inject('orbit-search-requested', ref(false))
// Wait for the input ref as route transitions can mount the catalogue later.
watch([searchRequested, searchInput], ([requested, input]) => {
  if (!requested || !input) return
  input.focus({ preventScroll: true })
  input.scrollIntoView({ block: 'center', behavior: 'instant' })
  searchRequested.value = false
}, { flush: 'post' })
const { getLocalizedText } = useLocalized()
const {
  loading, products, categories, selectedCategory, searchQuery, currentPage, totalPages,
  showFilterDrawer, categoryGroups, categoryMap, selectCategory, loadProducts,
  changePage, clearSearch, initialize, cleanup,
} = useProductList({ pageSize: 12, homeRouteName: 'products', latestRequestOnly: true })
const title = computed(() => getLocalizedText(categoryMap.value.get(selectedCategory.value || 0)?.name) || t('nav.products'))
const options = computed(() => categoryGroups.value.flatMap(group => [
  { id: group.id, name: getLocalizedText(group.name) },
  ...group.children.map(child => ({ id: child.id, name: `${getLocalizedText(group.name)} / ${getLocalizedText(child.name)}` })),
]))
// The shared list loader catches errors. A successful response replaces the
// products array, including an empty response; an unchanged array marks failure.
const failed = ref(false)
let beforeRequest = products.value
watch(loading, value => {
  if (value) { beforeRequest = products.value; failed.value = false }
  else failed.value = beforeRequest === products.value
}, { flush: 'sync' })
const isDesktopGrid = useMediaQuery('(min-width: 1024px)')
const isMobile = useMediaQuery('(max-width: 767px)')
const categoryStrip = ref<HTMLElement>()
const categoryIndicator = ref({ width: '0px', transform: 'translateX(0px)', opacity: 0 })
let categoryObserver: ResizeObserver | undefined
const syncCategoryIndicator = () => {
  const strip = categoryStrip.value
  if (!strip || !isMobile.value) return
  const active = strip.querySelector<HTMLElement>('button[aria-pressed="true"]')
  if (!active) return
  categoryIndicator.value = { width: `${active.offsetWidth}px`, transform: `translateX(${active.offsetLeft}px)`, opacity: 1 }
  const left = Math.max(0, active.offsetLeft - (strip.clientWidth - active.offsetWidth) / 2)
  strip.scrollTo({ left, behavior: reducedMotion.value ? 'instant' : 'smooth' })
}
watch([selectedCategory, options, isMobile], async () => {
  await nextTick()
  syncCategoryIndicator()
}, { flush: 'post' })
onMounted(() => {
  categoryObserver = new ResizeObserver(syncCategoryIndicator)
  if (categoryStrip.value) categoryObserver.observe(categoryStrip.value)
  syncCategoryIndicator()
})
onUnmounted(() => categoryObserver?.disconnect())
const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
const desktopGrid = ref<HTMLElement>()
const mobileGrid = ref<HTMLElement>()
let mobileAnimation: Animation | undefined
watch(products, () => {
  mobileAnimation?.cancel()
  if (!isMobile.value || reducedMotion.value || currentPage.value !== 1 || !mobileGrid.value) return
  mobileAnimation = mobileGrid.value.animate([
    { opacity: .55, transform: 'translateY(4px)' }, { opacity: 1, transform: 'none' },
  ], { duration: 220, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' })
}, { flush: 'post' })
watch([isMobile, reducedMotion], () => mobileAnimation?.cancel())
onUnmounted(() => mobileAnimation?.cancel())
let gridAnimation: Animation | undefined
watch(products, () => {
  gridAnimation?.cancel()
  if (!isDesktopGrid.value || reducedMotion.value || !desktopGrid.value) return
  gridAnimation = desktopGrid.value.animate([
    { opacity: .35, transform: 'translateY(6px)' },
    { opacity: 1, transform: 'translateY(0)' },
  ], { duration: 300, easing: 'cubic-bezier(0.22, 1, 0.36, 1)' })
}, { flush: 'post' })
watch([isDesktopGrid, reducedMotion], () => gridAnimation?.cancel())
onUnmounted(() => gridAnimation?.cancel())
const mobileProducts = ref<any[]>([])
watch(products, value => {
  mobileProducts.value = currentPage.value === 1 ? value : [...mobileProducts.value, ...value].filter((item, index, all) => all.findIndex(other => other.id === item.id) === index)
}, { flush: 'sync' })
const pages = computed(() => {
  const start = Math.max(1, Math.min(currentPage.value - 2, totalPages.value - 4))
  return Array.from({ length: Math.min(5, totalPages.value) }, (_, index) => start + index)
})
const resetFilters = () => { clearSearch(); selectCategory(null, true) }
const refresh = () => { currentPage.value = 1; void loadProducts() }
const loadMore = () => {
  if (loading.value) return
  if (failed.value) { void loadProducts(); return }
  if (currentPage.value < totalPages.value) { currentPage.value += 1; void loadProducts() }
}
const mobileLoadTrigger = ref<HTMLElement>()
useIntersectionObserver(mobileLoadTrigger, ([entry]) => {
  if (entry?.isIntersecting && !failed.value && window.matchMedia('(max-width: 1023px)').matches) loadMore()
}, { rootMargin: '120px' })
let touchStart = 0
const startRefresh = (event: TouchEvent) => { touchStart = window.scrollY <= 0 ? (event.touches[0]?.clientY || 0) : 0 }
const endRefresh = (event: TouchEvent) => {
  if (touchStart && (event.changedTouches[0]?.clientY || 0) - touchStart > 90 && !loading.value) refresh()
  touchStart = 0
}
usePageSeo({ canonicalPath: () => route.path, title: () => title.value })
let restoreFrame = 0
let restoreTimer: ReturnType<typeof setTimeout> | undefined
let unmounted = false
onBeforeRouteLeave((to, from) => {
  if (isMobile.value && to.name === 'product-detail' && !loading.value && !failed.value) {
    mobileBrowseSnapshot = {
      path: from.fullPath, savedAt: Date.now(), scrollY: window.scrollY,
      selectedCategory: selectedCategory.value, searchQuery: searchQuery.value,
      currentPage: currentPage.value, products: [...products.value], mobileProducts: [...mobileProducts.value], totalPages: totalPages.value,
    }
  } else mobileBrowseSnapshot = undefined
})
onMounted(async () => {
  const snapshot = isMobile.value && mobileBrowseSnapshot?.path === route.fullPath
    && Date.now() - mobileBrowseSnapshot.savedAt < 120_000 ? mobileBrowseSnapshot : undefined
  mobileBrowseSnapshot = undefined
  await initialize(snapshot)
  if (snapshot) mobileProducts.value = snapshot.mobileProducts
  if (unmounted || !snapshot || searchRequested.value) return
  await nextTick()
  // Router's scroll reset precedes the entering transition; restore after layout settles.
  restoreFrame = requestAnimationFrame(() => {
    window.scrollTo({ top: snapshot.scrollY, behavior: 'instant' })
    restoreTimer = setTimeout(() => window.scrollTo({ top: snapshot.scrollY, behavior: 'instant' }), 240)
  })
})
onUnmounted(() => {
  unmounted = true
  cancelAnimationFrame(restoreFrame)
  clearTimeout(restoreTimer)
  cleanup()
})
</script>

<template>
  <div class="orbit-catalog mx-auto max-w-[1200px] px-5 pb-14 pt-7 sm:px-8 lg:pt-12" @touchstart.passive="startRefresh" @touchend.passive="endRefresh">
    <nav class="orbit-catalog-breadcrumb mb-7 flex items-center gap-2 text-xs text-muted-foreground" :aria-label="t('breadcrumb.label')">
      <RouterLink to="/" class="inline-flex min-h-11 items-center hover:text-primary">{{ t('nav.home') }}</RouterLink><ChevronRight class="h-3 w-3" /><span>{{ t('nav.products') }}</span>
    </nav>
    <h1 class="sr-only">{{ title }}</h1>
    <div class="orbit-catalog-toolbar mb-8 flex flex-wrap items-center gap-3">
      <div class="relative min-w-0 flex-1 lg:max-w-md">
        <Search class="pointer-events-none absolute left-4 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <input ref="searchInput" v-model="searchQuery" type="search" class="h-12 w-full rounded-xl border border-border bg-card pl-11 pr-12 text-sm outline-none focus:border-primary" :placeholder="t('products.searchBoxPlaceholder')" :aria-label="t('products.searchLabel')" />
        <button v-if="searchQuery" type="button" class="absolute right-0 top-0 grid h-12 w-12 place-items-center rounded-xl text-muted-foreground" :aria-label="t('blog.searchClear')" @click="clearSearch"><X class="h-4 w-4" /></button>
      </div>
      <select :value="selectedCategory ?? ''" class="hidden h-12 w-80 min-w-64 max-w-full shrink-0 rounded-xl border border-border bg-card px-4 text-sm lg:block" :title="options.find(option => option.id === selectedCategory)?.name || t('orbit.catalog.all')" :aria-label="t('orbit.catalog.category')" @change="selectCategory(Number(($event.target as HTMLSelectElement).value) || null)">
        <option value="">{{ t('orbit.catalog.all') }}</option><option v-for="option in options" :key="option.id" :value="option.id">{{ option.name }}</option>
      </select>
      <button type="button" class="flex h-12 items-center gap-2 rounded-xl border border-border bg-card px-4 text-sm lg:hidden" @click="showFilterDrawer = true"><SlidersHorizontal class="h-4 w-4" /><span class="sr-only">{{ t('orbit.catalog.filter') }}</span><span v-if="selectedCategory" class="h-1.5 w-1.5 rounded-full bg-primary" /></button>
      <button type="button" class="hidden h-12 w-12 place-items-center rounded-xl text-muted-foreground hover:bg-secondary lg:grid" :disabled="loading" :aria-label="t('orbit.actions.refresh')" @click="refresh"><RotateCw class="h-4 w-4" /></button>
    </div>
    <nav ref="categoryStrip" class="orbit-category-strip" :aria-label="t('orbit.catalog.category')">
      <span class="orbit-category-indicator" :style="categoryIndicator" aria-hidden="true" />
      <button type="button" :aria-pressed="!selectedCategory" @click="selectCategory(null)">{{ t('orbit.catalog.all') }}</button>
      <button v-for="option in options" :key="option.id" type="button" :aria-pressed="selectedCategory === option.id" @click="selectCategory(option.id)">{{ option.name }}</button>
    </nav>
    <div v-if="selectedCategory" class="orbit-selected-category mb-6 flex items-center gap-2 text-sm text-muted-foreground"><span>{{ title }}</span><button class="grid h-11 w-11 place-items-center rounded-xl" :aria-label="t('products.clearFilters')" @click="selectCategory(null)"><X class="h-4 w-4" /></button></div>
    <div v-if="failed" role="alert" class="mb-6 rounded-[20px] border border-border bg-card p-6 text-center">
      <p class="text-muted-foreground">{{ t('orbit.catalog.failed') }}</p><button type="button" class="mt-4 min-h-11 rounded-xl bg-primary px-5 text-sm font-medium text-primary-foreground" @click="loadProducts">{{ t('errorBoundary.retry') }}</button>
    </div>
    <div v-if="loading && currentPage === 1 && (!isDesktopGrid && !isMobile || !products.length)" class="orbit-catalog-skeleton grid gap-5 lg:grid-cols-3" role="status" :aria-label="t('common.loading')"><div v-for="index in 6" :key="index" class="h-[390px] rounded-[20px] bg-secondary motion-safe:animate-pulse" /></div>
    <template v-else-if="products.length || mobileProducts.length">
      <div ref="mobileGrid" class="orbit-mobile-grid grid grid-cols-1 gap-5 lg:hidden" :aria-busy="loading" :inert="isMobile && loading && currentPage === 1"><OrbitProductCard v-for="product in mobileProducts" :key="product.id" :product="product" /></div>
      <div ref="desktopGrid" class="orbit-desktop-grid hidden gap-5 lg:grid lg:grid-cols-3" :aria-busy="loading"><OrbitProductCard v-for="product in products" :key="product.id" :product="product" /></div>
      <nav v-if="totalPages > 1" class="mt-10 hidden justify-center gap-2 lg:flex" :aria-label="t('orbit.catalog.pagination')">
        <button class="grid h-11 w-11 place-items-center rounded-xl bg-card disabled:opacity-40" :aria-label="t('orbit.catalog.previous')" :disabled="currentPage <= 1 || loading" @click="changePage(currentPage - 1)"><ChevronLeft class="h-4 w-4" /></button>
        <button v-for="page in pages" :key="page" class="h-11 min-w-11 rounded-xl px-3 text-sm" :class="page === currentPage ? 'bg-primary text-primary-foreground' : 'bg-card text-foreground'" :aria-current="page === currentPage ? 'page' : undefined" :disabled="loading" @click="changePage(page)">{{ page }}</button>
        <button class="grid h-11 w-11 place-items-center rounded-xl bg-card disabled:opacity-40" :aria-label="t('orbit.catalog.next')" :disabled="currentPage >= totalPages || loading" @click="changePage(currentPage + 1)"><ChevronRight class="h-4 w-4" /></button>
      </nav>
      <button v-if="currentPage < totalPages" ref="mobileLoadTrigger" type="button" class="mt-6 min-h-12 w-full rounded-xl bg-card text-sm text-primary disabled:opacity-50 lg:hidden" :disabled="loading" @click="loadMore">{{ loading ? t('common.loading') : t('orbit.catalog.loadMore') }}</button>
    </template>
    <div v-else-if="!failed" class="rounded-[20px] bg-card px-6 py-20 text-center"><PackageOpen class="mx-auto mb-4 h-10 w-10 stroke-1 text-muted-foreground" /><p class="text-muted-foreground">{{ searchQuery || selectedCategory ? t('products.emptyFiltered') : t('products.empty') }}</p><button v-if="searchQuery || selectedCategory" class="mt-5 min-h-11 rounded-xl px-4 text-primary" @click="resetFilters">{{ t('products.clearFilters') }}</button></div>
    <OrbitSheet v-model:open="showFilterDrawer" :title="t('orbit.catalog.category')">
      <div class="space-y-1"><button class="min-h-12 w-full rounded-xl px-4 text-left" :class="!selectedCategory ? 'bg-secondary text-primary' : ''" @click="selectCategory(null, true)">{{ t('orbit.catalog.all') }}</button><button v-for="option in options" :key="option.id" class="min-h-12 w-full rounded-xl px-4 py-3 text-left text-sm" :class="selectedCategory === option.id ? 'bg-secondary text-primary' : ''" @click="selectCategory(option.id, true)">{{ option.name }}</button><p v-if="!categories.length" class="px-4 py-3 text-sm text-muted-foreground">{{ t('orbit.catalog.noCategories') }}</p></div>
    </OrbitSheet>
  </div>
</template>

<style scoped>
.orbit-category-strip { display: none; }
@media (min-width: 1024px) {
  .orbit-desktop-grid { transition: opacity 180ms ease; }
  .orbit-desktop-grid[aria-busy='true'] { opacity: .5; pointer-events: none; }
}
@media (max-width: 767px) {
  .orbit-catalog { padding: 16px 16px 32px; }
  .orbit-catalog-breadcrumb, .orbit-selected-category { display: none; }
  .orbit-catalog-toolbar { margin-bottom: 10px; gap: 8px; }
  .orbit-catalog-toolbar input { height: 44px; border-color: transparent; background: var(--ui-bg-muted); border-radius: 12px; }
  .orbit-catalog-toolbar > button { height: 44px; width: 44px; padding: 0; justify-content: center; border-color: transparent; }
  .orbit-category-strip { position: relative; isolation: isolate; display: flex; gap: 4px; overflow-x: auto; margin-bottom: 16px; padding: 4px; border-radius: 999px; background: var(--ui-bg-muted); scrollbar-width: none; }
  .orbit-category-strip::-webkit-scrollbar { display: none; }
  .orbit-category-indicator { position: absolute; z-index: -1; left: 0; top: 4px; bottom: 4px; border-radius: 999px; background: var(--ui-bg-elevated); box-shadow: var(--orbit-segment-shadow); transition: transform 360ms var(--orbit-ease), width 360ms var(--orbit-ease); pointer-events: none; }
  .orbit-category-strip button { flex-shrink: 0; white-space: nowrap; min-height: 44px; padding: 8px 16px; border-radius: 999px; font-size: 13px; font-weight: 550; color: var(--ui-text-secondary); transition: color 180ms ease; }
  .orbit-category-strip button:focus-visible { outline-offset: -3px; }
  .orbit-category-strip button[aria-pressed='true'] { color: var(--ui-text-primary); }
  .orbit-mobile-grid { transition: opacity 160ms ease; }
  .orbit-mobile-grid[inert] { opacity: .5; }
  .orbit-mobile-grid, .orbit-catalog-skeleton { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
  .orbit-catalog-skeleton > div { height: 260px; }
}
</style>
