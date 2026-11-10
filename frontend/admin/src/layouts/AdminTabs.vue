<script setup lang="ts">
import { nextTick, ref, watch } from 'vue'
import { useRoute, useRouter, type RouteLocationResolved } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { X, PanelsTopLeft } from 'lucide-vue-next'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/components/ui/select'
import { useAdminTabsStore, type AdminTab } from '@/stores/adminTabs'
import { useAdminAuthStore } from '@/stores/auth'
import { notifyError } from '@/utils/notify'

const props = defineProps<{ navigation: { label: string; to: string }[] }>()
const route = useRoute()
const router = useRouter()
const store = useAdminTabsStore()
const auth = useAdminAuthStore()
const { t } = useI18n()
const strip = ref<HTMLElement | null>(null)
const busy = ref(false)
const action = ref('')

/** 复用路由主权限与替代权限语义。@param target 已解析路由 @returns 是否可访问 */
function canAccess(target: RouteLocationResolved) {
  const permission = typeof target.meta.permission === 'string' ? target.meta.permission : ''
  const alternatives = Array.isArray(target.meta.permissionAny) ? target.meta.permissionAny : []
  return Boolean(target.matched.length && target.meta.requiresAuth && target.meta.adminTab !== false)
    && (!permission || [permission, ...alternatives].some(value => typeof value === 'string' && auth.hasPermission(value)))
}

/** 标签标题复用菜单，详情附路径参数区分。@param tab 标签 @returns 本地化标题 */
function title(tab: AdminTab) {
  const exact = props.navigation.find(item => router.resolve(item.to).path === tab.path)
  if (exact) return exact.label
  const target = router.resolve(tab.fullPath)
  if (typeof target.meta.tabTitle === 'string') return t(target.meta.tabTitle)
  const parent = [...props.navigation].sort((left, right) => right.to.length - left.to.length)
    .find(item => item.to !== '/' && tab.path.startsWith(`${router.resolve(item.to).path}/`))
  const detail = Object.values(target.params).flat().join(' / ')
  return `${parent?.label ?? t('admin.layout.workspace')}${detail ? ` · ${detail}` : ''}`
}

/** 根据现有入口找可访问默认页。@returns 默认路由地址 */
function fallback() {
  return ['/', ...props.navigation.map(item => item.to)].find(path => canAccess(router.resolve(path))) ?? '/forbidden'
}

/** 仅登记已成功完成的路由并让当前标签进入视口。@returns Promise<void> */
async function syncRoute() {
  if (canAccess(router.resolve(route.fullPath))) store.add({ path: route.path, fullPath: route.fullPath })
  await nextTick()
  strip.value?.querySelector<HTMLElement>('[aria-current="page"]')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
}
watch(() => route.fullPath, syncRoute, { immediate: true })

/** 导航错误只给出操作反馈，不输出可能含敏感信息的完整地址。@param destination 目标 @returns 是否完成 */
async function navigate(destination: string) {
  if (router.resolve(destination).fullPath === route.fullPath) return true
  try { return !(await router.push(destination)) }
  catch { notifyError(t('adminTabs.failed')); return false }
}

/** 舒课安全关闭流程：先导航成功，再移除缓存名单。@param retained 保留标签 @param destination 回退地址 @returns Promise<void> */
async function applyClosed(retained: AdminTab[], destination: string) {
  if (busy.value) return
  busy.value = true
  try {
    const previous = route.path
    if (!retained.some(tab => tab.path === previous)) {
      if (!(await navigate(destination))) return
      if (route.path === previous && router.resolve(destination).path !== previous) return
    }
    // 成功守卫可能重定向；以实际活动页为准，保留监听器登记的目标。
    const active = store.tabs.find(tab => tab.path === route.path)
    if (active && !retained.some(tab => tab.path === active.path) && previous === active.path) {
      store.refreshing = active.path
    }
    store.retain([...retained.map(tab => tab.path), ...(active ? [active.path] : [])])
    await syncRoute()
  } finally { busy.value = false }
}

/** 关闭单个标签。@param path 待关闭路径 @returns Promise<void> */
async function close(path: string) {
  const index = store.tabs.findIndex(tab => tab.path === path)
  const adjacent = store.tabs[index + 1] ?? store.tabs[index - 1]
  await applyClosed(store.tabs.filter(tab => tab.path !== path), adjacent?.fullPath ?? fallback())
}

/** 执行标签菜单操作。@param value 操作键 @returns Promise<void> */
async function perform(value: unknown) {
  action.value = String(value ?? '')
  if (value === 'refresh') {
    if (!busy.value && !store.refreshing) store.refreshing = route.path
  } else if (value === 'current') await close(route.path)
  else if (value === 'others') await applyClosed(store.tabs.filter(tab => tab.path === route.path), route.fullPath)
  else if (value === 'all') await applyClosed([], fallback())
  await nextTick()
  action.value = ''
}

watch(() => auth.token, (token, previous) => {
  if (token !== previous) store.clear()
  if (token) void syncRoute()
})
watch(() => JSON.stringify([auth.permissionsLoaded, auth.permissions, auth.isSuper]), async () => {
  if (!auth.permissionsLoaded) return
  const allowed = store.tabs.filter(tab => canAccess(router.resolve(tab.fullPath)))
  store.retain(allowed.map(tab => tab.path))
  store.epoch++
  if (!canAccess(router.resolve(route.fullPath))) await navigate(fallback())
  else await syncRoute()
})
</script>

<template>
  <nav class="flex min-w-0 flex-1 items-center" :aria-label="t('adminTabs.navigation')">
    <div ref="strip" class="flex min-w-0 flex-1 overflow-x-auto gap-1.5">
      <div v-for="tab in store.tabs" :key="tab.path" class="flex h-10 shrink-0 items-center rounded-md border text-sm font-medium" :class="tab.path === route.path ? 'border-border bg-secondary font-semibold text-foreground' : 'border-transparent text-muted-foreground hover:bg-secondary/60'">
        <button type="button" class="h-full max-w-52 truncate px-4 focus-visible:outline-ring" :aria-current="tab.path === route.path ? 'page' : undefined" :title="title(tab)" :disabled="busy || Boolean(store.refreshing)" @click="navigate(tab.fullPath)">{{ title(tab) }}</button>
        <button type="button" class="mr-1.5 flex h-7 w-7 items-center justify-center rounded hover:bg-muted focus-visible:outline-ring" :aria-label="`${t('adminTabs.close')}: ${title(tab)}`" :disabled="busy || Boolean(store.refreshing)" @click="close(tab.path)"><X class="h-3.5 w-3.5" /></button>
      </div>
    </div>
    <Select :model-value="action" :disabled="busy || Boolean(store.refreshing)" @update:model-value="perform">
      <SelectTrigger hide-icon class="ml-2 h-8 w-8 shrink-0 justify-center p-0" :title="t('adminTabs.more')" :aria-label="t('adminTabs.more')"><PanelsTopLeft class="h-4 w-4" aria-hidden="true" /></SelectTrigger>
      <SelectContent>
        <SelectItem v-for="key in ['refresh', 'current', 'others', 'all']" :key="key" :value="key">{{ t(`adminTabs.${key}`) }}</SelectItem>
      </SelectContent>
    </Select>
  </nav>
</template>
