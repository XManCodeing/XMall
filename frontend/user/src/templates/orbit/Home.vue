<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import { ArrowRight, ArrowUpRight, ChevronLeft, ChevronRight, PackageOpen } from 'lucide-vue-next'
import { categoryAPI, postAPI, productAPI } from '@/api'
import { useAppStore } from '@/stores/app'
import { useLocalized } from '@/composables/useProduct'
import { usePageSeo } from '@/composables/usePageSeo'
import { useBannerCarousel } from '@/composables/useBannerCarousel'
import { useAnnouncement, type HomeAnnouncement } from '@/composables/useAnnouncement'
import { buildCategoryGroups, type PublicCategory } from '@/utils/category'
import { getImageUrl } from '@/utils/image'
import AnnouncementModal from '@/components/AnnouncementModal.vue'
import OrbitProductCard from './components/OrbitProductCard.vue'
import OrbitProducts from './Products.vue'

const { t } = useI18n()
const app = useAppStore()
const isListMode = computed(() => app.config?.template_mode === 'list')
const isMobile = useMediaQuery('(max-width: 767px)')
const { getLocalizedText } = useLocalized()
const products = ref<any[]>([])
const categories = ref<PublicCategory[]>([])
const posts = ref<any[]>([])
const loading = ref(true)
const failed = ref(false)
const { shouldShow } = useAnnouncement()
const activeAnnouncement = ref<HomeAnnouncement | null>(null)
const announcementVisible = ref(false)
const { heroBanner, heroImage, bannerCount, hasHeroLink, currentBannerIndex, loadBanners, goToHeroLink, handlePrevHeroBanner, handleNextHeroBanner, stopHeroAutoPlay } = useBannerCarousel()
const loadProducts = async () => {
  loading.value = true
  failed.value = false
  try { const response = await productAPI.list({ page: 1, page_size: 6 }); products.value = response.data.data || [] }
  catch { failed.value = true }
  finally { loading.value = false }
}
const loadCategories = async () => {
  try { const response = await categoryAPI.list(); categories.value = buildCategoryGroups(response.data.data || []) }
  catch { categories.value = [] }
}
const loadPosts = async () => {
  const builtin = app.config?.nav_config?.builtin
  const blog = builtin?.blog !== false
  const notice = builtin?.notice !== false
  if (!blog && !notice) return
  try {
    const response = await postAPI.list({ page: 1, page_size: 3, ...(!blog ? { type: 'notice' } : !notice ? { type: 'blog' } : {}) })
    posts.value = response.data.data || []
  } catch { posts.value = [] }
}
usePageSeo({ canonicalPath: () => '/' })
onMounted(async () => {
  await app.loadConfig()
  const announcement = app.config?.announcement as HomeAnnouncement | undefined
  if (announcement && shouldShow(announcement)) { activeAnnouncement.value = announcement; announcementVisible.value = true }
  if (isListMode.value) return
  await Promise.all([loadProducts(), loadCategories(), loadPosts(), loadBanners()])
  // No automatic slide changes: browsing and reduced-motion preferences take priority.
  stopHeroAutoPlay()
})
onUnmounted(stopHeroAutoPlay)
const changeBanner = (direction: number) => { direction > 0 ? handleNextHeroBanner() : handlePrevHeroBanner(); stopHeroAutoPlay() }
</script>

<template>
  <div v-if="isListMode || isMobile">
    <OrbitProducts />
    <AnnouncementModal v-if="activeAnnouncement" :announcement="activeAnnouncement" :visible="announcementVisible" @update:visible="announcementVisible = $event" />
  </div>
  <div v-else class="mx-auto max-w-[1200px] px-5 pb-16 sm:px-8">
    <h1 class="sr-only">{{ t('nav.home') }}</h1>

    <section v-if="categories.length" class="py-9 lg:py-12">
      <div class="mb-5 flex items-end justify-between gap-4"><h2 class="text-xl font-semibold tracking-tight">{{ t('orbit.catalog.category') }}</h2><RouterLink to="/products" class="inline-flex min-h-11 items-center gap-1 text-sm text-primary">{{ t('orbit.catalog.all') }}<ChevronRight class="h-4 w-4" /></RouterLink></div>
      <div class="grid grid-cols-1 gap-2 lg:grid-cols-4 lg:gap-4">
        <RouterLink v-for="category in categories.slice(0, 8)" :key="category.id" :to="`/categories/${category.slug}`" class="flex min-h-[64px] items-center gap-3 rounded-[20px] bg-card px-4 py-3 transition-colors hover:bg-secondary lg:min-h-[88px] lg:px-5">
          <img v-if="category.icon" :src="getImageUrl(category.icon)" alt="" loading="lazy" class="h-9 w-9 shrink-0 rounded-xl object-contain" />
          <span class="min-w-0 flex-1 break-words text-sm font-medium">{{ getLocalizedText(category.name) }}</span><ArrowUpRight class="h-4 w-4 shrink-0 text-muted-foreground" />
        </RouterLink>
      </div>
    </section>

    <section v-if="heroBanner" class="py-5" :aria-label="t('orbit.home.highlights')">
      <div class="overflow-hidden rounded-[20px] bg-card">
        <button v-if="hasHeroLink" class="block w-full text-left" @click="goToHeroLink"><img v-if="heroImage" :src="heroImage" :alt="getLocalizedText(heroBanner.title)" class="max-h-[360px] w-full object-cover" /><span class="flex min-h-12 items-center justify-between gap-4 px-6 py-4 text-sm font-medium">{{ getLocalizedText(heroBanner.title) || t('common.learnMore') }}<ArrowUpRight class="h-4 w-4 shrink-0 text-primary" /></span></button>
        <template v-else><img v-if="heroImage" :src="heroImage" :alt="getLocalizedText(heroBanner.title)" class="max-h-[360px] w-full object-cover" /><p v-if="getLocalizedText(heroBanner.title)" class="px-6 py-4 text-sm font-medium">{{ getLocalizedText(heroBanner.title) }}</p></template>
        <div v-if="bannerCount > 1" class="flex items-center justify-end gap-3 px-4 pb-3"><span class="text-xs tabular-nums text-muted-foreground">{{ currentBannerIndex + 1 }} / {{ bannerCount }}</span><button class="grid h-11 w-11 place-items-center rounded-xl bg-secondary" :aria-label="t('common.previousBanner')" @click="changeBanner(-1)"><ChevronLeft class="h-4 w-4" /></button><button class="grid h-11 w-11 place-items-center rounded-xl bg-secondary" :aria-label="t('common.nextBanner')" @click="changeBanner(1)"><ChevronRight class="h-4 w-4" /></button></div>
      </div>
    </section>

    <section class="py-9 lg:py-12">
      <div class="mb-7 flex flex-wrap items-end justify-between gap-4"><h2 class="text-xl font-semibold tracking-tight">{{ t('nav.products') }}</h2><RouterLink to="/products" class="inline-flex min-h-11 items-center gap-2 text-sm text-primary">{{ t('home.featured.viewAll') }}<ArrowRight class="h-4 w-4" /></RouterLink></div>
      <div v-if="loading" class="grid gap-5 lg:grid-cols-3" role="status" :aria-label="t('common.loading')"><div v-for="index in 3" :key="index" class="h-[390px] rounded-[20px] bg-secondary motion-safe:animate-pulse" /></div>
      <div v-else-if="failed" role="alert" class="rounded-[20px] bg-card p-10 text-center"><p class="text-muted-foreground">{{ t('orbit.catalog.failed') }}</p><button class="mt-5 min-h-11 rounded-xl bg-primary px-5 text-sm text-primary-foreground" @click="loadProducts">{{ t('errorBoundary.retry') }}</button></div>
      <div v-else-if="products.length" class="grid grid-cols-1 gap-5 lg:grid-cols-3"><OrbitProductCard v-for="product in products" :key="product.id" :product="product" /></div>
      <div v-else class="rounded-[20px] bg-card px-6 py-16 text-center text-muted-foreground"><PackageOpen class="mx-auto mb-4 h-10 w-10 stroke-1" /><p>{{ t('products.empty') }}</p></div>
    </section>

    <section v-if="posts.length" class="border-t border-border pt-10">
      <h2 class="mb-6 text-xl font-semibold tracking-tight">{{ t('home.latest.title') }}</h2>
      <div class="grid grid-cols-1 gap-4 lg:grid-cols-3"><RouterLink v-for="post in posts" :key="post.id" :to="`/blog/${post.slug}`" class="rounded-[20px] bg-card p-6"><p v-if="post.published_at" class="text-xs text-muted-foreground">{{ new Date(post.published_at).toLocaleDateString(app.locale) }}</p><h3 class="mt-3 line-clamp-2 text-lg font-semibold tracking-tight">{{ getLocalizedText(post.title) }}</h3><p class="mt-3 line-clamp-2 text-sm leading-relaxed text-muted-foreground">{{ getLocalizedText(post.summary) }}</p><span class="mt-5 inline-flex min-h-11 items-center gap-2 text-sm text-primary">{{ t('blog.readMore') }}<ArrowUpRight class="h-4 w-4" /></span></RouterLink></div>
    </section>
    <AnnouncementModal v-if="activeAnnouncement" :announcement="activeAnnouncement" :visible="announcementVisible" @update:visible="announcementVisible = $event" />
  </div>
</template>