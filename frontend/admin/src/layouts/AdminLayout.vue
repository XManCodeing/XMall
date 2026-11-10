<script setup lang="ts">
import { parseNavExpansion, resolveNavExpansion } from '@/utils/navExpansion'
import AdminBrandHeader from './AdminBrandHeader.vue'
import { useSystemUpdateNotice } from '@/composables/useSystemUpdateNotice'
import { computed, onBeforeUnmount, onMounted, provide, ref, watch } from 'vue'
import { pageDialogTargetKey } from '@/components/ui/dialog/pageScope'
import ConfirmDialogHost from '@/components/ConfirmDialogHost.vue'
import { useRoute, RouterLink } from 'vue-router'
import AdminTabs from './AdminTabs.vue'
import AdminRouteCache from './AdminRouteCache.vue'
import { LayoutDashboard, LogOut, ChevronDown, ChevronRight, ChevronsLeft, ChevronsRight, Home, Languages, CircleHelp } from 'lucide-vue-next'
import MembershipGuideDialog from '@/components/telegram/MembershipGuideDialog.vue'
import { useMembershipGuide } from '@/composables/useMembershipGuide'
import { useAdminNavigation, type NavGroup } from '@/composables/useAdminNavigation'
import { Menu } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet'
import SystemUpdateDialog from '@/components/SystemUpdateDialog.vue'
import { useAdminAuthStore } from '@/stores/auth'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { applySiteIcon } from '@/utils/favicon'
import { adminUrl } from '@/utils/adminBase'

const NAV_GROUP_EXPANDED_STORAGE_KEY = 'admin_nav_group_expanded_v2'
const SIDEBAR_COLLAPSED_STORAGE_KEY = 'admin_sidebar_collapsed'
const SIDEBAR_AUTO_COLLAPSE_WIDTH = 1280 // xl breakpoint

const readExpandedGroups = () => {
  if (typeof window === 'undefined') return {}
  try {
    const raw = localStorage.getItem(NAV_GROUP_EXPANDED_STORAGE_KEY)
    if (raw !== null) return parseNavExpansion(raw)
    const migrated = parseNavExpansion(localStorage.getItem('admin_nav_group_expanded'), true)
    localStorage.setItem(NAV_GROUP_EXPANDED_STORAGE_KEY, JSON.stringify(migrated))
    return migrated
  } catch {
    return {}
  }
}

const { t, locale } = useI18n({ useScope: 'global' })
const guide = useMembershipGuide()
const guideDialog = ref<InstanceType<typeof MembershipGuideDialog> | null>(null)
const route = useRoute()
const contentElement = ref<HTMLElement | null>(null)
const dialogTarget = ref<HTMLElement | null>(null)
provide(pageDialogTargetKey, computed(() => dialogTarget.value))
const authStore = useAdminAuthStore()
const isDark = ref(false)
const appVersion = ref('')
const siteUrl = ref('')

// Manual upgrade actions remain in the dialog; background checks only update the badge.
const updateCheckOpen = ref(false)
const { hasUpdate, latestVersion, canCheck, accept: acceptUpdateCheck } = useSystemUpdateNotice(updateCheckOpen)

const navSearch = ref('')
const expandedGroups = ref<Record<string, boolean>>(readExpandedGroups())
const mobileNavOpen = ref(false)
const sidebarCollapsed = ref(false)
const sidebarUserToggled = ref(false) // tracks if user manually toggled

// Close mobile nav on route change
watch(() => route.path, () => {
  mobileNavOpen.value = false
})

const { navGroups } = useAdminNavigation()

watch(
  expandedGroups,
  (value) => {
    if (typeof window === 'undefined') {
      return
    }
    localStorage.setItem(NAV_GROUP_EXPANDED_STORAGE_KEY, JSON.stringify(value))
  },
  { deep: true }
)

const normalizedSearch = computed(() => navSearch.value.trim().toLowerCase())
const dashboardNavLabel = computed(() => t('admin.navItems.dashboard'))

const showDashboardNav = computed(() => {
  const keyword = normalizedSearch.value
  if (!keyword) {
    return true
  }
  const label = dashboardNavLabel.value.toLowerCase()
  const groupLabel = t('admin.navGroups.dashboard').toLowerCase()
  return label.includes(keyword) || groupLabel.includes(keyword)
})

const filteredNavGroups = computed<NavGroup[]>(() => {
  const keyword = normalizedSearch.value
  if (!keyword) {
    return navGroups.value
  }
  const result: NavGroup[] = []
  navGroups.value.forEach((group) => {
    const groupMatched = group.label.toLowerCase().includes(keyword)
    const matchedItems = groupMatched
      ? group.items
      : group.items.filter((item) => item.label.toLowerCase().includes(keyword))
    if (matchedItems.length > 0) {
      result.push({
        ...group,
        items: matchedItems,
      })
    }
  })
  return result
})

const isGroupExpanded = (groupID: string) => {
  return resolveNavExpansion(
    expandedGroups.value[groupID],
    navGroups.value.some((group) => group.id === groupID && group.items.some((item) => isItemActive(item.to))),
    Boolean(normalizedSearch.value),
  )
}

const toggleGroup = (groupID: string) => {
  expandedGroups.value[groupID] = !isGroupExpanded(groupID)
}

const splitNavTarget = (to: string) => {
  const [path, query = ''] = to.split('?')
  return { path, query }
}

const navQueryMatches = (query: string) => {
  if (!query) return true
  const params = new URLSearchParams(query)
  for (const [key, value] of params.entries()) {
    const routeValue = route.query[key]
    const normalized = Array.isArray(routeValue) ? routeValue[0] : routeValue
    if (String(normalized ?? '') !== value) return false
  }
  return true
}

const isItemActive = (to: string) => {
  const target = splitNavTarget(to)
  if (target.path?.startsWith('/telegram-bot/support/') && route.path.startsWith('/telegram-bot/support/')) return true
  if (target.path === '/') {
    return route.path === '/'
  }
  if (target.query) {
    return route.path === target.path && navQueryMatches(target.query)
  }
  // 精确匹配优先：如果当前路由被其他更具体的导航项精确匹配，则前缀匹配项不应高亮
  if (route.path === target.path) {
    for (const group of navGroups.value) {
      for (const item of group.items) {
        const other = splitNavTarget(item.to)
        if (item.to !== to && other.path === route.path && other.query && navQueryMatches(other.query)) {
          return false
        }
      }
    }
    return true
  }
  if (!route.path.startsWith(`${target.path}/`)) return false
  // 前缀匹配时，检查是否有其他导航项精确匹配当前路由
  for (const group of navGroups.value) {
    for (const item of group.items) {
      const other = splitNavTarget(item.to)
      if (item.to !== to && route.path === other.path && (!other.query || navQueryMatches(other.query))) return false
    }
  }
  return true
}

const applyTheme = (theme: 'light' | 'dark') => {
  isDark.value = theme === 'dark'
  const root = document.documentElement
  if (theme === 'dark') {
    root.classList.add('dark')
  } else {
    root.classList.remove('dark')
  }
  localStorage.setItem('admin_theme', theme)
}

const toggleTheme = () => {
  applyTheme(isDark.value ? 'light' : 'dark')
}

const applyLocale = (value: string) => {
  locale.value = value
  localStorage.setItem('admin_locale', value)
}

const toggleSidebar = () => {
  sidebarCollapsed.value = !sidebarCollapsed.value
  sidebarUserToggled.value = true
  localStorage.setItem(SIDEBAR_COLLAPSED_STORAGE_KEY, String(sidebarCollapsed.value))
}

const handleResize = () => {
  if (sidebarUserToggled.value) return
  sidebarCollapsed.value = window.innerWidth < SIDEBAR_AUTO_COLLAPSE_WIDTH
}

const handleLogout = () => {
  authStore.logout()
  // 必须走 adminUrl()：后台可被挂到运行时任意 web.admin_path 前缀下，
  // 硬编码 '/login' 会跳出后台落到用户前台 SPA 的兜底路由上。
  window.location.href = adminUrl('/login')
}

onMounted(() => {
  const savedTheme = localStorage.getItem('admin_theme')
  if (savedTheme === 'light' || savedTheme === 'dark') {
    applyTheme(savedTheme)
  } else {
    const prefersDark = window.matchMedia?.('(prefers-color-scheme: dark)').matches
    applyTheme(prefersDark ? 'dark' : 'light')
  }

  const savedLocale = localStorage.getItem('admin_locale')
  if (savedLocale) {
    applyLocale(savedLocale)
  }

  // Initialize sidebar collapse state
  const savedCollapsed = localStorage.getItem(SIDEBAR_COLLAPSED_STORAGE_KEY)
  if (savedCollapsed !== null) {
    sidebarCollapsed.value = savedCollapsed === 'true'
    sidebarUserToggled.value = true
  } else {
    // Auto-collapse on medium screens
    sidebarCollapsed.value = window.innerWidth < SIDEBAR_AUTO_COLLAPSE_WIDTH
  }
  window.addEventListener('resize', handleResize)

  // Fetch app version and site URL
  adminAPI.getPublicConfig().then((res) => {
    const payload = res.data?.data
    applySiteIcon(payload?.brand?.site_icon)
    const ver = payload?.app_version
    if (typeof ver === 'string') {
      appVersion.value = ver
    }
    const url = payload?.brand?.site_url
    if (typeof url === 'string' && url) {
      siteUrl.value = url
    }
  }).catch(() => {})
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', handleResize)
})
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <div class="flex min-h-screen md:h-screen md:overflow-hidden">
      <!-- Desktop sidebar -->
      <aside
        class="hidden md:flex border-r border-border bg-card flex-col sticky top-0 h-screen overflow-y-auto transition-all duration-300"
        :class="sidebarCollapsed ? 'w-16' : 'w-64'"
      >
        <AdminBrandHeader :collapsed="sidebarCollapsed" :dark="isDark" :can-check="canCheck" :has-update="hasUpdate" :latest-version="latestVersion" @update="updateCheckOpen = true" @theme="toggleTheme" />
        <div v-if="!sidebarCollapsed" class="px-3 pb-2">
          <Input
            v-model="navSearch"
            class="h-8 text-xs"
            :placeholder="t('admin.navSearch.placeholder')"
          />
        </div>
        <nav class="pb-3 space-y-2 flex-1 overflow-y-auto" :class="sidebarCollapsed ? 'px-1.5' : 'px-3'">
          <!-- Expanded mode -->
          <template v-if="!sidebarCollapsed">
            <RouterLink
              v-if="showDashboardNav"
              to="/"
              class="flex items-center gap-3 rounded-lg px-3 py-2 text-sm transition-colors"
              :class="isItemActive('/') ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:text-foreground hover:bg-secondary/70'"
            >
              <LayoutDashboard class="h-4 w-4 shrink-0" />
              <span>{{ dashboardNavLabel }}</span>
            </RouterLink>
            <div
              v-if="!showDashboardNav && filteredNavGroups.length === 0"
              class="rounded-lg border border-dashed border-border px-3 py-4 text-xs text-muted-foreground"
            >
              {{ t('admin.navSearch.empty') }}
            </div>
            <div v-for="group in filteredNavGroups" :key="group.id" class="space-y-1">
              <button
                type="button"
                class="flex w-full items-center justify-between rounded-lg px-3 py-2 text-sm transition-colors hover:bg-secondary/70"
                :class="isGroupExpanded(group.id) ? 'bg-secondary/40 text-foreground' : 'text-foreground'"
                @click="toggleGroup(group.id)"
              >
                <div class="flex min-w-0 items-center gap-3">
                  <component :is="group.icon" class="h-4 w-4 shrink-0" />
                  <span class="truncate">{{ group.label }}</span>
                </div>
                <component
                  :is="isGroupExpanded(group.id) ? ChevronDown : ChevronRight"
                  class="h-4 w-4 shrink-0 text-muted-foreground"
                />
              </button>
              <div v-show="isGroupExpanded(group.id)" class="space-y-1 pl-9">
                <RouterLink
                  v-for="item in group.items"
                  :key="`${group.id}-${item.to}`"
                  :to="item.to"
                  class="flex items-center gap-2 rounded-lg px-3 py-2 text-sm transition-colors"
                  :class="isItemActive(item.to) ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:bg-secondary/70 hover:text-foreground'"
                >
                  <component v-if="item.icon" :is="item.icon" class="h-3.5 w-3.5 shrink-0" />
                  <span class="truncate">{{ item.label }}</span>
                </RouterLink>
              </div>
            </div>
          </template>
          <!-- Collapsed mode: icon-only -->
          <template v-else>
            <RouterLink
              to="/"
              class="flex items-center justify-center rounded-lg p-2 transition-colors"
              :class="isItemActive('/') ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:text-foreground hover:bg-secondary/70'"
              :title="dashboardNavLabel"
            >
              <LayoutDashboard class="h-4 w-4 shrink-0" />
            </RouterLink>
            <template v-for="group in navGroups" :key="`collapsed-${group.id}`">
              <div class="my-1 border-t border-border/50" />
              <RouterLink
                v-for="item in group.items"
                :key="`collapsed-${group.id}-${item.to}`"
                :to="item.to"
                class="flex items-center justify-center rounded-lg p-2 transition-colors"
                :class="isItemActive(item.to) ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:text-foreground hover:bg-secondary/70'"
                :title="item.label"
              >
                <component v-if="item.icon" :is="item.icon" class="h-4 w-4 shrink-0" />
              </RouterLink>
            </template>
          </template>
        </nav>
        <!-- Compact sidebar footer -->
        <div
          class="flex h-12 shrink-0 items-center gap-2 border-t border-border/60"
          :class="sidebarCollapsed ? 'justify-center px-2' : 'justify-between px-4'"
        >
          <span v-if="!sidebarCollapsed" class="min-w-0 truncate text-xs tabular-nums text-muted-foreground" :title="appVersion">
            {{ appVersion }}
          </span>
          <button
            type="button"
            class="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
            @click="toggleSidebar"
            :title="sidebarCollapsed ? t('admin.layout.expandSidebar') : t('admin.layout.collapseSidebar')"
            :aria-label="sidebarCollapsed ? t('admin.layout.expandSidebar') : t('admin.layout.collapseSidebar')"
            :aria-expanded="!sidebarCollapsed"
          >
            <ChevronsLeft v-if="!sidebarCollapsed" class="h-4 w-4" aria-hidden="true" />
            <ChevronsRight v-else class="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </aside>

      <!-- Mobile sidebar (Sheet) -->
      <Sheet scope="global" v-model:open="mobileNavOpen">
        <SheetContent side="left" class="w-72 p-0 flex flex-col">
          <SheetTitle class="sr-only">{{ t('admin.layout.navigation') }}</SheetTitle>
          <AdminBrandHeader class="mr-8" :dark="isDark" :can-check="canCheck" :has-update="hasUpdate" :latest-version="latestVersion" @update="updateCheckOpen = true; mobileNavOpen = false" @theme="toggleTheme" />
          <div class="px-3 pb-2">
            <Input
              v-model="navSearch"
              class="h-8 text-xs"
              :placeholder="t('admin.navSearch.placeholder')"
            />
          </div>
          <nav class="px-3 pb-3 space-y-2 flex-1 overflow-y-auto">
            <RouterLink
              v-if="showDashboardNav"
              to="/"
              class="flex items-center gap-3 rounded-lg px-3 py-2 text-sm transition-colors"
              :class="isItemActive('/') ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:text-foreground hover:bg-secondary/70'"
            >
              <LayoutDashboard class="h-4 w-4 shrink-0" />
              <span>{{ dashboardNavLabel }}</span>
            </RouterLink>
            <div
              v-if="!showDashboardNav && filteredNavGroups.length === 0"
              class="rounded-lg border border-dashed border-border px-3 py-4 text-xs text-muted-foreground"
            >
              {{ t('admin.navSearch.empty') }}
            </div>
            <div v-for="group in filteredNavGroups" :key="`mobile-${group.id}`" class="space-y-1">
              <button
                type="button"
                class="flex w-full items-center justify-between rounded-lg px-3 py-2 text-sm transition-colors hover:bg-secondary/70"
                :class="isGroupExpanded(group.id) ? 'bg-secondary/40 text-foreground' : 'text-foreground'"
                @click="toggleGroup(group.id)"
              >
                <div class="flex min-w-0 items-center gap-3">
                  <component :is="group.icon" class="h-4 w-4 shrink-0" />
                  <span class="truncate">{{ group.label }}</span>
                </div>
                <component
                  :is="isGroupExpanded(group.id) ? ChevronDown : ChevronRight"
                  class="h-4 w-4 shrink-0 text-muted-foreground"
                />
              </button>
              <div v-show="isGroupExpanded(group.id)" class="space-y-1 pl-9">
                <RouterLink
                  v-for="item in group.items"
                  :key="`mobile-${group.id}-${item.to}`"
                  :to="item.to"
                  class="flex items-center gap-2 rounded-lg px-3 py-2 text-sm transition-colors"
                  :class="isItemActive(item.to) ? 'bg-secondary text-foreground' : 'text-muted-foreground hover:bg-secondary/70 hover:text-foreground'"
                >
                  <component v-if="item.icon" :is="item.icon" class="h-3.5 w-3.5 shrink-0" />
                  <span class="truncate">{{ item.label }}</span>
                </RouterLink>
              </div>
            </div>
          </nav>
          <div v-if="appVersion" class="px-6 py-4 border-t border-border text-[11px] text-muted-foreground">
            {{ appVersion }}
          </div>
        </SheetContent>
      </Sheet>

      <div class="flex-1 flex flex-col min-w-0">
        <header class="shrink-0 flex min-w-0 items-center border-b border-border bg-background px-4 md:px-8 py-3 md:py-4 gap-2">
          <Button size="icon-sm" variant="ghost" class="shrink-0 md:hidden" @click="mobileNavOpen = true">
            <Menu class="h-5 w-5" />
          </Button>
          <AdminTabs :navigation="[{ label: dashboardNavLabel, to: '/' }, ...navGroups.flatMap(group => group.items)]" />
          <div class="flex shrink-0 items-center gap-2">
            <Button v-if="guide.visible.value" size="icon-sm" variant="outline" :title="t('membershipGuide.guide')" :aria-label="t('membershipGuide.guide')" @click="guideDialog?.showCurrent()">
              <CircleHelp class="h-4 w-4" aria-hidden="true" />
            </Button>
            <Select
              :model-value="locale"
              @update:modelValue="(value) => { if (value) applyLocale(String(value)) }"
            >
              <SelectTrigger hide-icon class="h-8 w-8 justify-center p-0" :title="t('membershipGuide.language')" :aria-label="t('membershipGuide.language')">
                <Languages class="h-4 w-4" aria-hidden="true" />
                <SelectValue class="sr-only" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="zh-CN">{{ t('admin.common.lang.zhCN') }}</SelectItem>
                <SelectItem value="zh-TW">{{ t('admin.common.lang.zhTW') }}</SelectItem>
                <SelectItem value="en-US">{{ t('admin.common.lang.enUS') }}</SelectItem>
              </SelectContent>
            </Select>
            <a v-if="siteUrl" :href="siteUrl" target="_blank" rel="noopener noreferrer">
              <Button size="icon-sm" variant="outline" :title="t('admin.layout.homePage')">
                <Home class="h-4 w-4" />
              </Button>
            </a>
            <Button variant="outline" size="icon-sm" :title="t('admin.common.logout')" :aria-label="t('admin.common.logout')" @click="handleLogout">
              <LogOut class="h-4 w-4" aria-hidden="true" />
            </Button>
          </div>
        </header>
        <div class="relative isolate flex min-h-0 flex-1 flex-col">
          <main ref="contentElement" class="min-h-0 min-w-0 flex-1 overflow-x-hidden md:overflow-y-auto px-4 py-4 md:px-8 md:py-6">
            <div class="min-w-0">
              <AdminRouteCache :scroll-element="contentElement" :dialog-target="dialogTarget" />
            </div>
          </main>
          <div ref="dialogTarget" class="pointer-events-none absolute inset-0 z-40 overflow-hidden" data-page-dialog-host />
        </div>
      </div>
    </div>

    <MembershipGuideDialog ref="guideDialog" v-model:open="guide.open.value" v-model:step="guide.step.value" />
    <ConfirmDialogHost />
    <SystemUpdateDialog v-model:open="updateCheckOpen" @checked="acceptUpdateCheck" />
  </div>
</template>
