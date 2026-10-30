import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'

const source = (path: string) => readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')

test('Orbit support uses site contact, stays clear of bottom controls, and is hidden on About and with the keyboard', () => {
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  assert.match(layout, /config\?\.contact\?\.telegram\?\.trim\(\)/)
  assert.ok(layout.includes("(telegramSupportUrl || whatsappSupportUrl) && route.name !== 'about' && !keyboardOpen"))
  assert.match(layout, /config\?\.contact\?\.whatsapp\?\.trim\(\)/)
  assert.match(layout, /url.hostname === 'wa.me'/)
  assert.match(layout, /orbit-support-dock/)
  assert.match(layout, /orbit-support-bubble orbit-whatsapp-support/)
  assert.match(layout, /\['t.me', 'telegram.me'\]\.includes\(url.hostname\)/)
  const css = source('templates/orbit/styles/orbit.css')
  assert.match(css, /bottom: max\(28dvh, calc\(var\(--orbit-bottom-offset\) \+ 96px\)\)/)
  assert.match(css, /background: var\(--orbit-support-bg\)/)
  const tokens = source('style.css')
  for (const token of ['--orbit-support-bg', '--orbit-support-hover', '--orbit-support-text', '--orbit-whatsapp-bg', '--orbit-whatsapp-hover', '--orbit-whatsapp-text']) {
    assert.equal(tokens.split(`${token}:`).length - 1, 2)
  }
  assert.match(source('templates/orbit/About.vue'), /<ClassicAbout hide-telegram/)
  assert.match(source('views/About.vue'), /hideTelegram: \(\) => props.hideTelegram === true/)
  assert.match(source('composables/useAbout.ts'), /telegram: options.hideTelegram\?\.\(\) \? '' : contact.telegram/)
  assert.match(source('composables/useAbout.ts'), /whatsapp: options.hideWhatsapp\?\.\(\) \? '' : contact.whatsapp/)
  assert.match(source('templates/orbit/About.vue'), /hide-telegram hide-whatsapp/)
})

test('Orbit mobile support docks compact icons without reducing the touch target', () => {
  const css = source('templates/orbit/styles/orbit.css')
  const mobile = css.slice(css.indexOf('@media (max-width: 767px) {'), css.indexOf('/* Mobile purchase'))
  assert.match(mobile, /\.orbit-support-dock \{ gap: 4px; \}/)
  assert.match(mobile, /min-width: 44px/)
  assert.match(mobile, /min-height: 44px/)
  assert.match(mobile, /width: 32px/)
  assert.match(mobile, /\.orbit-support-bubble > span \{ display: none; \}/)
  assert.match(mobile, /\.orbit-whatsapp-support::before \{ background: var\(--orbit-whatsapp-bg\); \}/)
  const desktop = css.slice(0, css.indexOf('@media (max-width: 767px) {'))
  assert.match(desktop, /min-width: 56px/)
  assert.doesNotMatch(desktop, /\.orbit-support-bubble > span/)
})

test('Orbit header omits desktop branding while preserving centered navigation and mobile titles', () => {
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  assert.doesNotMatch(layout, /orbit-desktop-brand|BrandMark|brandLogo/)
  assert.match(layout, /orbit-mobile-page-title/)
  const css = source('templates/orbit/styles/orbit.css')
  assert.match(css, /\.orbit-desktop-nav \{ grid-column: 2;/)
  assert.match(css, /\.orbit-header-actions \{ grid-column: 3;/)
})

test('Orbit list mode delegates Home to the full catalogue instead of six featured products', () => {
  const home = source('templates/orbit/Home.vue')
  assert.match(home, /template_mode === 'list'/)
  assert.match(home, /v-if="isListMode \|\| isMobile"/)
  assert.match(home, /<OrbitProducts/)
  assert.match(home, /useMediaQuery\('\(max-width: 767px\)'\)/)
  assert.match(home, /if \(isListMode.value\) return/)
  const products = source('templates/orbit/Products.vue')
  assert.match(products, /useProductList/)
})

test('Orbit mobile purchase bars share the layout navigation offset', () => {
  const tokens = source('style.css')
  assert.match(tokens, /--orbit-bottom-offset: var\(--orbit-mobile-nav-height\)/)
  assert.match(tokens, /--orbit-mobile-nav-height: var\(--orbit-tab-height\)/)
  for (const name of ['Cart', 'Checkout', 'Payment', 'RechargeOrderDetail']) {
    assert.match(source(`templates/orbit/${name}.vue`), /bottom: var\(--orbit-bottom-offset/)
  }
  assert.match(source('templates/orbit/ProductDetail.vue'), /bottom: var\(--orbit-mobile-nav-height/)
})

test('Orbit moves only page headings into the mobile header and cleans up its observer', () => {
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  const css = source('templates/orbit/styles/orbit.css')
  assert.match(layout, /orbit-mobile-page-title/)
  assert.match(layout, /characterData: true/)
  assert.match(layout, /titleObserver\?\.disconnect\(\)/)
  assert.match(layout, /\.prose, \.rich-text-content, dialog/)
  assert.match(css, /\.orbit-main \[data-orbit-page-heading\]/)
  assert.doesNotMatch(css, /\.orbit-main h1\s*\{[^}]*display:\s*none/)
  const catalogue = source('templates/orbit/Products.vue')
  assert.match(catalogue, /<h1 class="sr-only">/)
  assert.doesNotMatch(catalogue, /orbit-mobile-title|orbit-catalog-heading/)
  const home = source('templates/orbit/Home.vue')
  const headings = [...home.matchAll(/<h2 class="([^"]+)"/g)].map(match => match[1])
  assert.equal(headings.length, 3)
  for (const heading of headings) assert.match(heading!, /text-xl font-semibold tracking-tight/)
})

test('Orbit desktop navigation preserves configured links and tracks resizing and nested routes', () => {
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  assert.match(layout, /v-for="item in primaryNavItems"/)
  assert.match(layout, /v-else :href="item.path" :target="item.target"/)
  assert.match(layout, /router.resolve\(item.path\)/)
  assert.match(layout, /new ResizeObserver\(updateIndicator\)/)
  assert.match(layout, /navObserver\?\.disconnect\(\)/)
  assert.match(layout, /cancelAnimationFrame\(indicatorFrame\)/)
  assert.match(layout, /useMediaQuery\('\(min-width: 768px\)'\)/)
  assert.match(layout, /if \(reducedMotion.value\)/)
  assert.match(layout, /onUnmounted\(finishCloseMenu\)/)
})

test('Orbit desktop capsule includes a reactive account entry and shares its active indicator', () => {
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  const desktopItems = layout.slice(layout.indexOf('const primaryNavItems = computed'), layout.indexOf('const desktopNav'))
  assert.match(desktopItems, /items\.push\(\{/)
  assert.match(desktopItems, /key: 'account', path: auth\.isAuthenticated \? '\/me' : '\/auth\/login'/)
  assert.match(desktopItems, /label: t\(auth\.isAuthenticated \? 'navbar.personalCenter' : 'navbar.login'\)/)
  assert.ok(layout.includes("if (/^\\/(auth|me)(\\/|$)/.test(route.path)) return 'account'"))
  assert.match(layout, /watch\(\[activeNavKey, primaryNavItems, isDesktop\]/)
  const mobileNav = layout.slice(layout.indexOf('<nav class="orbit-tabbar"'), layout.indexOf('<dialog'))
  assert.equal((mobileNav.match(/<RouterLink/g) || []).length, 4)
  assert.doesNotMatch(mobileNav, /primaryNavItems/)
})

test('Orbit desktop palette is root-scoped, responsive, and supplies both appearances', () => {
  const css = source('style.css')
  const start = css.indexOf('/* Orbit palette')
  const palette = css.slice(start, css.indexOf('\nhtml {', start))
  assert.match(palette, /@media \(min-width: 0px\)/)
  assert.match(palette, /:root.orbit-theme\.dark/)
  for (const token of ['--orbit-action-bg', '--orbit-action-text', '--orbit-segment-shadow', '--orbit-scrim']) {
    assert.equal(palette.split(`${token}:`).length - 1, 2)
  }
  const orbit = source('templates/orbit/styles/orbit.css')
  const desktop = orbit.slice(orbit.indexOf('/* Desktop motion'))
  assert.match(desktop, /@media \(min-width: 768px\)/)
  assert.match(desktop, /transform 480ms var\(--orbit-ease\)/)
  assert.match(desktop, /prefers-reduced-motion: reduce/)
  assert.doesNotMatch(desktop, /#[0-9a-f]{3,8}\b/i)
})

test('Orbit catalogue motion leaves the mobile loader intact and cancels obsolete animations', () => {
  const catalogue = source('templates/orbit/Products.vue')
  assert.match(catalogue, /!isDesktopGrid && !isMobile \|\| !products.length/)
  assert.match(catalogue, /if \(!isDesktopGrid.value \|\| reducedMotion.value/)
  assert.match(catalogue, /onUnmounted\(\(\) => gridAnimation\?\.cancel\(\)\)/)
  assert.match(catalogue, /watch\(\[isDesktopGrid, reducedMotion\]/)
})

test('Orbit exposes the catalogue in navigation without a redundant detail back link', () => {
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  assert.match(layout, /configuredNavItems.value.filter\(item => item.key !== 'products'\)/)
  assert.match(layout, /items.splice\(1, 0/)
  assert.match(layout, /key: 'products', path: '\/products', label: t\('orbit.nav.products'\)/)
  assert.doesNotMatch(layout, /isListMode.value \? 'home' : 'products'/)
  assert.doesNotMatch(source('templates/orbit/ProductDetail.vue'), /orbit-detail-back|productDetail.backToProducts|backToList/)
})

test('Orbit mobile category motion, sheets and return snapshots are bounded and accessible', () => {
  const catalogue = source('templates/orbit/Products.vue')
  assert.match(catalogue, /orbit-category-indicator/)
  assert.match(catalogue, /:inert="isMobile && loading && currentPage === 1"/)
  assert.match(catalogue, /categoryObserver\?\.disconnect\(\)/)
  assert.match(catalogue, /Date.now\(\) - mobileBrowseSnapshot.savedAt < 120_000/)
  assert.match(catalogue, /to.name === 'product-detail'/)
  assert.match(catalogue, /clearTimeout\(restoreTimer\)/)
  const sheet = source('templates/orbit/components/OrbitSheet.vue')
  assert.match(sheet, /current !== revision/)
  assert.match(sheet, /isMobile.value && !reducedMotion.value/)
  assert.match(sheet, /previousFocus\?\.isConnected/)
  assert.match(sheet, /event.clientY < bounds.top/)
  assert.match(sheet, /prefers-reduced-motion: reduce/)
})

test('Orbit catalogue ignores stale responses and restores browsing without triggering filter reloads', () => {
  const list = source('composables/useProductList.ts')
  assert.match(list, /options.latestRequestOnly && \(disposed \|\| version !== requestVersion\)/)
  assert.match(list, /if \(options.latestRequestOnly\) \{ requestVersion \+= 1; loading.value = true \}/)
  assert.match(list, /await nextTick\(\)\s+initializing = false/)
  assert.match(list, /disposed = true/)
})

test('Orbit product imagery uses theme-root brightness tokens without changing source assets', () => {
  const tokens = source('style.css')
  for (const token of ['--orbit-image-brightness', '--orbit-detail-image-brightness']) {
    assert.equal(tokens.split(`${token}:`).length - 1, 2)
  }
  assert.match(source('templates/orbit/components/OrbitProductCard.vue'), /filter: brightness\(var\(--orbit-image-brightness, 1\)\)/)
  assert.match(source('templates/orbit/ProductDetail.vue'), /filter: brightness\(var\(--orbit-detail-image-brightness, 1\)\)/)
})

test('Orbit mobile purchase card keeps its heading and bounded inline SKU selection', () => {
  const detail = source('templates/orbit/ProductDetail.vue')
  assert.match(detail, /data-orbit-keep-heading/)
  assert.match(detail, /isMobile && activeSkus.length <= 4/)
  assert.match(detail, /!isMobile \|\| activeSkus.length > 4/)
  assert.match(detail, /:disabled="!isSkuPurchasable\(sku\)"/)
  assert.match(detail, /@click="chooseSku\(sku\)"/)
  assert.match(source('templates/orbit/layout/OrbitLayout.vue'), /heading.removeAttribute\('data-orbit-page-heading'\)/)
})

test('Orbit mobile navigation and checkout guards stay scoped to Orbit mobile', () => {
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  assert.match(layout, /!isDesktop && showMobileBack/)
  assert.match(layout, /safeOrbitHistoryPath\(window.history.state\?\.back\)/)
  assert.match(layout, /resolved.name !== 'not-found'/)
  const checkout = source('templates/orbit/Checkout.vue')
  assert.match(checkout, /if \(!isMobile.value\) return true/)
  assert.match(checkout, /if \(submitting.value\) return false/)
  assert.match(checkout, /onBeforeRouteLeave\(protectForm\)/)
  assert.match(checkout, /removeEventListener\('beforeunload', beforeUnload\)/)
  assert.match(source('templates/orbit/Payment.vue'), /isMobile.value && to.path === '\/checkout'/)
})

test('Orbit mobile purchase bar adds to cart directly without a more menu', () => {
  const detail = source('templates/orbit/ProductDetail.vue')
  assert.match(detail, /orbit-add-to-cart[^\n]+text-destructive[^\n]+:disabled="!canPurchase" @click="addToCart"><ShoppingCart/)
  assert.doesNotMatch(detail, /moreOpen|addFromSheet|MoreHorizontal/)
})

test('Orbit motion and viewport listeners clean up and avoid transformed fixed bars', () => {
  const viewport = source('templates/orbit/composables/useOrbitViewport.ts')
  assert.match(viewport, /orbitKeyboardVisible\(mobile, editing/)
  assert.match(viewport, /removeAfterEach\?\.\(\)/)
  assert.match(viewport, /removeEventListener\('focusin', updateViewport\)/)
  const layout = source('templates/orbit/layout/OrbitLayout.vue')
  assert.match(layout, /data-orbit-motion="direction"/)
  assert.match(layout, /orbit-keyboard-open/)
  assert.match(layout, /position: relative; left: 0; transform: none/)
  assert.match(layout, /prefers-reduced-motion: reduce/)
  const sheet = source('templates/orbit/components/OrbitSheet.vue')
  assert.match(sheet, /dragOffset.value >= 80/)
  assert.match(sheet, /@pointercancel="cancelDrag"/)
  assert.match(sheet, /touch-action: none/)
})

test('Orbit locale keys remain in sync across all three languages', () => {
  const paths = (value: Record<string, unknown>, prefix = ''): string[] => Object.entries(value).flatMap(([key, child]) =>
    child && typeof child === 'object' ? paths(child as Record<string, unknown>, `${prefix}${key}.`) : [`${prefix}${key}`],
  ).sort()
  const locales = ['zh-CN', 'zh-TW', 'en-US'].map(code => JSON.parse(source(`i18n/locales/${code}.json`)).orbit)
  assert.ok(locales[0])
  assert.deepEqual(paths(locales[0]), paths(locales[1]))
  assert.deepEqual(paths(locales[0]), paths(locales[2]))
})
