<script setup lang="ts">
import { computed, markRaw, nextTick, provide, ref, watch } from 'vue'
import { pageDialogTargetKey } from '@/components/ui/dialog/pageScope'
import { RouterView, useRoute, useRouter } from 'vue-router'
import { useAdminTabsStore } from '@/stores/adminTabs'
import { createRoutePageCache, ROUTE_CACHE_LIMIT } from './RoutePage'

const props = defineProps<{ scrollElement: HTMLElement | null; dialogTarget?: HTMLElement | null }>()
provide(pageDialogTargetKey, computed(() => props.dialogTarget ?? null))
const store = useAdminTabsStore()
const route = useRoute()
const router = useRouter()
const pages = markRaw(createRoutePageCache())
const visible = ref(true)
const include = computed(() => store.tabs.map(tab => tab.path).filter(path => path !== store.refreshing && router.resolve(path).meta.cachePage !== false))
const scrollPositions = new Map<string, number>()
let refreshGeneration = 0

watch(include, paths => {
  pages.retain(paths)
  for (const path of scrollPositions.keys()) if (!paths.includes(path)) scrollPositions.delete(path)
}, { flush: 'post' })
watch(() => store.epoch, () => { pages.clear(); scrollPositions.clear() }, { flush: 'sync' })
watch(() => route.path, async (path, previous) => {
  if (props.scrollElement) scrollPositions.set(previous, props.scrollElement.scrollTop)
  // 与实例缓存同样有界；淘汰后不冒充仍保留旧页面状态。
  const position = scrollPositions.get(path) ?? 0
  scrollPositions.delete(path)
  scrollPositions.set(path, position)
  while (scrollPositions.size > ROUTE_CACHE_LIMIT) scrollPositions.delete(scrollPositions.keys().next().value!)
  await nextTick()
  if (route.path === path && props.scrollElement) props.scrollElement.scrollTop = position
}, { flush: 'pre' })
watch(() => store.refreshing, async path => {
  if (!path) return
  const generation = ++refreshGeneration
  const epoch = store.epoch
  // 先排除 include 再移除 VNode，确保刷新不会复活旧实例。
  await nextTick()
  if (generation !== refreshGeneration || epoch !== store.epoch) return
  if (route.path !== path) { store.refreshing = ''; return }
  visible.value = false
  await nextTick()
  visible.value = true
  if (store.refreshing === path) store.refreshing = ''
  if (route.path === path && props.scrollElement) props.scrollElement.scrollTop = 0
})
</script>

<template>
  <RouterView v-slot="{ Component, route: current }">
    <KeepAlive :key="store.epoch" :include="include" :max="ROUTE_CACHE_LIMIT">
      <component :is="pages.get(current.path)" v-if="Component && visible" :key="current.path" :vnode="Component" :route="current" />
    </KeepAlive>
  </RouterView>
</template>
