<template>
  <div class="orbit-scope" :data-orbit-motion="direction" :class="{ 'orbit-keyboard-open': keyboardOpen }" :style="{ '--orbit-visible-height': viewportHeight ? `${viewportHeight}px` : '100dvh' }">
    <header class="orbit-header">
      <div class="orbit-header-inner">
        <RouterLink v-if="brandLogo" to="/" class="orbit-brand orbit-desktop-brand" :title="appStore.brandSiteName">
          <img :src="brandLogo" :alt="appStore.brandSiteName" />
        </RouterLink>
        <span class="orbit-mobile-page-title" :title="mobilePageTitle">{{ mobilePageTitle }}</span>
        <nav ref="desktopNav" class="orbit-desktop-nav" :aria-label="t('nav.home')">
          <span class="orbit-nav-indicator" :class="{ 'is-ready': indicatorReady }" :style="indicatorStyle" aria-hidden="true" />
          <template v-for="item in primaryNavItems" :key="item.key">
            <RouterLink v-if="item.type === 'route'" :to="item.path" :data-nav-key="item.key" :class="{ 'is-active': activeNavKey === item.key }">{{ item.label }}</RouterLink>
            <a v-else :href="item.path" :target="item.target" rel="noopener noreferrer">{{ item.label }}</a>
          </template>
        </nav>
        <div class="orbit-header-actions">
          <button v-if="!isDesktop && showMobileBack" type="button" class="orbit-icon-button orbit-search-button" :aria-label="t('orbit.actions.back')" @click="goBack"><ChevronLeft :size="22" /></button>
          <button v-else type="button" class="orbit-icon-button orbit-search-button" :aria-label="t('products.searchLabel')" @click="openSearch"><Search :size="19" /></button>
          <button type="button" class="orbit-icon-button" :aria-label="t('resellerConsole.common.toggleTheme')" @click="toggleTheme"><Sun v-if="theme === 'dark'" :size="19" /><Moon v-else :size="19" /></button>
          <RouterLink to="/cart" class="orbit-icon-button orbit-desktop-cart" :aria-label="t('navbar.cart')"><ShoppingBag :size="19" /><span v-if="cartStore.totalItems" class="orbit-count">{{ cartStore.totalItems }}</span></RouterLink>
          <button type="button" class="orbit-icon-button" :aria-label="t('navbar.more')" @click="openMenu"><Ellipsis :size="21" /></button>
        </div>
      </div>
    </header>

    <main ref="pageContent" class="orbit-main"><slot /></main>

    <footer class="orbit-footer">
      <div class="orbit-footer-inner">
        <div class="orbit-footer-links">
          <RouterLink to="/terms">{{ t('footer.terms') }}</RouterLink>
          <RouterLink to="/privacy">{{ t('footer.privacy') }}</RouterLink>
          <RouterLink v-if="!auth.isAuthenticated" to="/guest/orders">{{ t('navbar.guestOrders') }}</RouterLink>
          <a v-for="link in footerLinks" :key="link.name" :href="link.url" target="_blank" rel="noopener noreferrer">{{ link.name }}</a>
        </div>
      </div>
    </footer>

    <nav class="orbit-tabbar" :aria-label="t('navbar.more')">
      <RouterLink to="/" :class="{ 'is-active': route.path === '/' }"><House :size="21" /><span>{{ t('nav.home') }}</span></RouterLink>
      <RouterLink to="/products" :class="{ 'is-active': route.path.startsWith('/products') || route.path.startsWith('/categories/') }"><LayoutGrid :size="21" /><span>{{ t('nav.products') }}</span></RouterLink>
      <RouterLink to="/cart" :class="{ 'is-active': route.path === '/cart' }"><span class="relative"><ShoppingBag :size="21" /><span v-if="cartStore.totalItems" class="orbit-count">{{ cartStore.totalItems }}</span></span><span>{{ t('navbar.cart') }}</span></RouterLink>
      <RouterLink :to="auth.isAuthenticated ? '/me' : '/auth/login'" :class="{ 'is-active': route.path.startsWith('/me') || route.path.startsWith('/auth') }"><UserRound :size="21" /><span>{{ t('navbar.personalCenter') }}</span></RouterLink>
    </nav>

    <dialog ref="menu" class="orbit-menu" :aria-label="t('navbar.more')" @click="onBackdropClick" @cancel="onMenuCancel">
      <div class="orbit-menu-heading"><h2>{{ t('navbar.more') }}</h2><button type="button" class="orbit-icon-button" :aria-label="t('orbit.actions.close')" @click="closeMenu"><X :size="21" /></button></div>
      <div class="orbit-menu-group">
        <template v-for="item in secondaryNavItems" :key="item.key">
          <RouterLink v-if="item.type === 'route'" :to="item.path" @click="closeMenu"><component :is="item.icon" :size="18" />{{ item.label }}<ChevronRight :size="16" class="ml-auto" /></RouterLink>
          <a v-else :href="item.path" :target="item.target" rel="noopener noreferrer" @click="closeMenu"><component :is="item.icon" :size="18" />{{ item.label }}<ArrowUpRight :size="16" class="ml-auto" /></a>
        </template>
        <RouterLink :to="auth.isAuthenticated ? '/me' : '/auth/login'" @click="closeMenu"><UserRound :size="18" />{{ t(auth.isAuthenticated ? 'navbar.personalCenter' : 'navbar.login') }}<ChevronRight :size="16" class="ml-auto" /></RouterLink>
        <RouterLink v-if="!auth.isAuthenticated" to="/guest/orders" @click="closeMenu"><ClipboardList :size="18" />{{ t('navbar.guestOrders') }}<ChevronRight :size="16" class="ml-auto" /></RouterLink>
        <button v-else type="button" @click="logout"><LogOut :size="18" />{{ t('navbar.logout') }}</button>
      </div>
      <h3 class="orbit-menu-label">{{ t('navbar.selectLanguage') }}</h3>
      <div class="orbit-menu-group">
        <button v-for="language in languages" :key="language.code" type="button" :aria-pressed="appStore.locale === language.code" @click="changeLanguage(language.code)">{{ language.name }}<Check v-if="appStore.locale === language.code" :size="18" class="ml-auto text-primary" /></button>
      </div>
    </dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, provide, ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowUpRight, Check, ChevronLeft, ChevronRight, ClipboardList, Ellipsis, House, LayoutGrid, LogOut, Moon, Search, ShoppingBag, Sun, UserRound, X } from 'lucide-vue-next'
import { useAppStore } from '../../../stores/app'
import { useCartStore } from '../../../stores/cart'
import { useUserAuthStore } from '../../../stores/userAuth'
import { useNavConfig } from '../../../composables/useNavConfig'
import { useTheme } from '../../../utils/theme'
import { getImageUrl } from '../../../utils/image'
import { getLocalizedText } from '../../../utils/resellerSiteConfig'
import { useBuyNowStore } from '../../../stores/buyNow'
import { orbitRootPaths, orbitParentPath, safeOrbitHistoryPath } from '../navigation'
import { useOrbitViewport } from '../composables/useOrbitViewport'
import '../styles/orbit.css'

const { direction, keyboardOpen, viewportHeight } = useOrbitViewport()

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const buyNow = useBuyNowStore()
const showMobileBack = computed(() => !orbitRootPaths.has(route.path))
const goBack = () => {
  const parent = orbitParentPath(route.path, route.query, route.query.mode === 'buynow' ? buyNow.item?.slug : undefined)
  // Payment must never lead back to a form that can create another order.
  if (route.path === '/pay') { void router.replace(parent); return }
  const back = safeOrbitHistoryPath(window.history.state?.back)
  if (back && back !== route.fullPath) {
    const resolved = router.resolve(back)
    const safe = resolved.matched.length > 0 && resolved.name !== 'not-found'
      && !['/checkout', '/pay'].includes(resolved.path)
      && !resolved.path.startsWith('/auth/')
      && (!resolved.meta.requiresUserAuth || auth.isAuthenticated)
    if (safe) { router.back(); return }
  }
  void router.replace(parent)
}
const pageContent = ref<HTMLElement>()
const contentTitle = ref('')
const fallbackTitles: Record<string, string> = {
  home: 'nav.home', products: 'nav.products', 'category-products': 'nav.products',
  'product-detail': 'nav.products', cart: 'cart.title', checkout: 'checkout.title',
  payment: 'payment.title', 'guest-orders': 'guestOrders.title',
  'guest-order-detail': 'guestOrderDetail.title', 'order-detail': 'orderDetail.title',
  'recharge-order-detail': 'rechargeOrder.title', blog: 'nav.blog', 'blog-detail': 'nav.blog',
  notice: 'nav.notice', about: 'nav.about', terms: 'footer.terms', privacy: 'footer.privacy',
  'user-login': 'auth.login.title', 'user-register': 'auth.register.title',
  'user-forgot': 'auth.forgot.title', 'not-found': 'notFoundPage.title',
}
const mobilePageTitle = computed(() => {
  if (route.name === 'home') return t('nav.home')
  if (String(route.name).startsWith('personal-center')) {
    const section = String(route.name).replace('personal-center-', '')
    const tab = section === 'gift-cards' ? 'giftCard' : section
    return route.name === 'personal-center' ? t('navbar.personalCenter') : t(`personalCenter.tabs.${tab}`)
  }
  return contentTitle.value || t(fallbackTitles[String(route.name)] || 'nav.home')
})
// Adapt shared classic pages as well as native Orbit pages, without changing other themes.
// Observe asynchronous product/article titles and locale changes, not document.title (SEO).
const syncPageTitle = () => {
  const heading = [...(pageContent.value?.querySelectorAll('h1') || [])].find(element =>
    !element.closest('.prose, .rich-text-content, dialog, [role="dialog"], .page-fade-leave-active'),
  )
  if (heading?.hasAttribute('data-orbit-keep-heading')) heading.removeAttribute('data-orbit-page-heading')
  else heading?.setAttribute('data-orbit-page-heading', '')
  contentTitle.value = heading?.textContent?.trim() || ''
}
let titleObserver: MutationObserver | undefined
onMounted(() => {
  titleObserver = new MutationObserver(syncPageTitle)
  if (pageContent.value) titleObserver.observe(pageContent.value, { childList: true, subtree: true, characterData: true })
  syncPageTitle()
})
watch(() => route.fullPath, () => { contentTitle.value = '' })
onUnmounted(() => titleObserver?.disconnect())
const searchRequested = ref(false)
provide('orbit-search-requested', searchRequested)
const openSearch = async () => {
  const input = document.querySelector<HTMLInputElement>('.orbit-catalog input[type="search"]')
  // Keep focus inside the tap handler so mobile browsers can open the keyboard.
  if (input) {
    input.focus({ preventScroll: true })
    input.scrollIntoView({ block: 'center', behavior: 'instant' })
    return
  }
  searchRequested.value = true
  try {
    const failure = await router.push('/products')
    if (failure) searchRequested.value = false
  } catch {
    searchRequested.value = false
  }
}
const appStore = useAppStore()
const cartStore = useCartStore()
const auth = useUserAuthStore()
const { theme, toggleTheme } = useTheme()
const { primaryNavItems: configuredNavItems, secondaryNavItems } = useNavConfig()
// Desktop always exposes the catalogue, even when Home uses list mode.
const primaryNavItems = computed(() => {
  const items = configuredNavItems.value.filter(item => item.key !== 'products')
  items.splice(1, 0, {
    key: 'products', path: '/products', label: t('orbit.nav.products'),
    icon: LayoutGrid, type: 'route', target: '_self',
  })
  items.push({
    key: 'account', path: auth.isAuthenticated ? '/me' : '/auth/login',
    label: t(auth.isAuthenticated ? 'navbar.personalCenter' : 'navbar.login'),
    icon: UserRound, type: 'route', target: '_self',
  })
  return items
})
const desktopNav = ref<HTMLElement>()
const isDesktop = useMediaQuery('(min-width: 768px)')
const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
const activeNavKey = computed(() => {
  if (/^\/(auth|me)(\/|$)/.test(route.path)) return 'account'
  // Longest path wins for configured links; detail pages retain their parent tab.
  const matches = primaryNavItems.value.filter(item => {
    if (item.type !== 'route') return false
    const path = router.resolve(item.path).path.replace(/\/$/, '') || '/'
    return route.path === path || (path !== '/' && route.path.startsWith(`${path}/`))
  }).sort((a, b) => router.resolve(b.path).path.length - router.resolve(a.path).path.length)
  if (matches[0]) return matches[0].key
  if (/^\/(products|categories)(\/|$)/.test(route.path)) return 'products'
  return ''
})
const indicatorStyle = ref({ width: '0px', transform: 'translateX(0px)', opacity: 0 })
const indicatorReady = ref(false)
let navObserver: ResizeObserver | undefined
let indicatorFrame = 0
const updateIndicator = () => {
  const nav = desktopNav.value
  if (!nav || !isDesktop.value) return
  const active = [...nav.querySelectorAll<HTMLElement>('[data-nav-key]')].find(link => link.dataset.navKey === activeNavKey.value)
  indicatorStyle.value = active
    ? { width: `${active.offsetWidth}px`, transform: `translateX(${active.offsetLeft}px)`, opacity: 1 }
    : { ...indicatorStyle.value, opacity: 0 }
  if (active && (active.offsetLeft < nav.scrollLeft || active.offsetLeft + active.offsetWidth > nav.scrollLeft + nav.clientWidth)) {
    nav.scrollTo({ left: Math.max(0, active.offsetLeft - (nav.clientWidth - active.offsetWidth) / 2), behavior: reducedMotion.value ? 'instant' : 'smooth' })
  }
  cancelAnimationFrame(indicatorFrame)
  indicatorFrame = requestAnimationFrame(() => { indicatorReady.value = true })
}
watch([activeNavKey, primaryNavItems, isDesktop], async () => {
  await nextTick()
  navObserver?.disconnect()
  if (desktopNav.value) {
    navObserver?.observe(desktopNav.value)
    desktopNav.value.querySelectorAll('a').forEach(link => navObserver?.observe(link))
  }
  updateIndicator()
}, { flush: 'post' })
onMounted(() => {
  navObserver = new ResizeObserver(updateIndicator)
  if (desktopNav.value) {
    navObserver.observe(desktopNav.value)
    desktopNav.value.querySelectorAll('a').forEach(link => navObserver?.observe(link))
  }
  updateIndicator()
})
onUnmounted(() => {
  navObserver?.disconnect()
  cancelAnimationFrame(indicatorFrame)
})
const menu = ref<HTMLDialogElement>()
const brandLogo = computed(() => {
  const logo = String(appStore.config?.brand?.site_logo || '').trim()
  return logo ? getImageUrl(logo) : ''
})
const footerLinks = computed(() => {
  const links = appStore.config?.footer_links
  if (!Array.isArray(links)) return []
  return links.map((item: { name?: unknown; url?: unknown }) => ({
    name: typeof item.name === 'string' ? item.name.trim() : getLocalizedText(item.name as Record<string, string>, appStore.locale),
    url: String(item.url || '').trim(),
  })).filter(item => item.name && item.url)
})
const languages = [
  { code: 'zh-CN', name: '简体中文' },
  { code: 'zh-TW', name: '繁體中文' },
  { code: 'en-US', name: 'English' },
]
let menuCloseTimer: ReturnType<typeof setTimeout> | undefined
const finishCloseMenu = () => {
  clearTimeout(menuCloseTimer)
  menu.value?.close()
  menu.value?.classList.remove('is-closing')
}
const openMenu = () => {
  clearTimeout(menuCloseTimer)
  menu.value?.classList.remove('is-closing')
  menu.value?.showModal()
}
const closeMenu = () => {
  if (!menu.value?.open) return
  if (reducedMotion.value) { finishCloseMenu(); return }
  if (menu.value.classList.contains('is-closing')) return
  menu.value.classList.add('is-closing')
  menuCloseTimer = setTimeout(finishCloseMenu, 180)
}
const onMenuCancel = (event: Event) => {
  event.preventDefault()
  closeMenu()
}
const onBackdropClick = (event: MouseEvent) => {
  if (event.target !== menu.value) return
  const bounds = menu.value.getBoundingClientRect()
  if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) closeMenu()
}
const changeLanguage = (code: string) => {
  appStore.setLocale(code)
  closeMenu()
}
const logout = () => {
  auth.logout()
  closeMenu()
}
watch(() => route.fullPath, closeMenu)
onUnmounted(finishCloseMenu)
</script>

<style scoped>
@media (max-width: 767px) {
  .orbit-main { overflow-x: clip; }
  .orbit-main :deep(input), .orbit-main :deep(textarea) { scroll-margin-block: 88px 32px; }
  .orbit-main :deep(.page-fade-enter-active), .orbit-main :deep(.page-fade-leave-active) {
    position: relative; left: 0; transform: none;
    transition: opacity 220ms var(--orbit-ease), left 220ms var(--orbit-ease);
  }
  .orbit-main :deep(.page-fade-leave-active) { transition-duration: 100ms; pointer-events: none; }
  [data-orbit-motion='forward'] .orbit-main :deep(.page-fade-enter-from) { left: 14px; transform: none; }
  [data-orbit-motion='back'] .orbit-main :deep(.page-fade-enter-from) { left: -14px; transform: none; }
  [data-orbit-motion='neutral'] .orbit-main :deep(.page-fade-enter-from) { left: 0; transform: none; }
  .orbit-keyboard-open .orbit-tabbar,
  .orbit-keyboard-open :deep(.orbit-mobile-action),
  .orbit-keyboard-open :deep(.orbit-purchase-bar) { visibility: hidden; pointer-events: none; }
  .orbit-keyboard-open :deep(.orbit-sheet), .orbit-keyboard-open .orbit-menu { max-height: calc(var(--orbit-visible-height) - 24px); }
}
@media (max-width: 767px) and (prefers-reduced-motion: reduce) {
  .orbit-main :deep(.page-fade-enter-active), .orbit-main :deep(.page-fade-leave-active),
  [data-orbit-motion] .orbit-main :deep(.page-fade-enter-from) { left: 0; transition: none; transform: none; }
}
</style>
